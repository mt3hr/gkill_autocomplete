// Package llm はローカルの LLM へ問い合わせる。
//
// OpenAI 互換の /v1/chat/completions を叩くので、Ollama / llama.cpp /
// LM Studio のいずれでも動く。
//
// 接続先がローカルに限られていることの検証は config パッケージが行う。
// ここでは「渡されたものを送る」だけで、どこへ送ってよいかの判断はしない。
package llm

// 編集前に読む: .claude/skills/autocomplete-suggest/SKILL.md（この領域の不変条件の正本）

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// maxErrorBodyBytes はエラー時に読むレスポンス本文の上限。
const maxErrorBodyBytes = 512

// maxAnswerTokens は1回の応答に許す長さ。
//
// **これが無いと応答が終わらないことがある。** 判定は temperature 0 の
// 貪欲デコードなので、モデルが同じ判定を繰り返し続ける状態に入りうる。
// 上限を渡さないと llama.cpp 側は n_predict を実質無制限として扱い、
// 文脈長を使い切るまで生成し続けたうえで、閉じ括弧の無い JSON を返す
// (2026-08-12 の実測: 1件に8分08秒かけて約4,460トークンを生成し、
// ExtractJSONObject が対応する "}" を見つけられず ErrBadResponse になった)。
//
// 正しい応答は候補6個でも200トークン前後で終わるので、512 で足りる。
const maxAnswerTokens = 512

// 失敗の種類。呼び出し側が errors.Is で見分けて、
// **記録の中身を含まない決め打ちの理由**に置き換えるために使う。
//
// エラー本文をそのまま画面やログへ出すことはできない。
// LLM の応答には判定させた記録の中身が混ざりうるため。
var (
	// ErrUnreachable は LLM に接続できないこと。
	ErrUnreachable = errors.New("LLM に繋がりません")
	// ErrTimeout は応答が時間内に返らなかったこと。
	ErrTimeout = errors.New("LLM が時間切れになりました")
	// ErrRejected は LLM がエラー応答を返したこと。
	ErrRejected = errors.New("LLM がエラーを返しました")
	// ErrBadResponse は応答を解釈できなかったこと。
	ErrBadResponse = errors.New("LLM の応答を解釈できません")
)

// Role は発言者。
const (
	RoleSystem = "system"
	RoleUser   = "user"
)

// Client はローカル LLM への接続。
type Client struct {
	endpoint    string
	textModel   string
	visionModel string
	httpClient  *http.Client
}

// New は接続を作る。
func New(endpoint string, textModel string, visionModel string, timeout time.Duration) *Client {
	return &Client{
		endpoint:    endpoint,
		textModel:   textModel,
		visionModel: visionModel,
		httpClient:  &http.Client{Timeout: timeout},
	}
}

// TextModel は本文の判定に使うモデル名を返す。
func (c *Client) TextModel() string { return c.textModel }

// VisionModel は写真の判定に使うモデル名を返す。
func (c *Client) VisionModel() string { return c.visionModel }

// Part はメッセージの一部。文字列か画像のどちらか。
type Part struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL は画像の指定。ローカル LLM に渡すのでデータURLを使う。
type ImageURL struct {
	URL string `json:"url"`
}

// TextPart は文字列の部分を作る。
func TextPart(text string) Part {
	return Part{Type: "text", Text: text}
}

// ImagePart は画像の部分を作る。
//
// 画像はデータURLとして埋め込む。ファイルのパスや配信URLは渡さない
// (接続先に利用者のファイル構成を知らせないため)。
func ImagePart(imageBytes []byte, contentType string) Part {
	if contentType == "" {
		contentType = "image/jpeg"
	}
	encoded := base64.StdEncoding.EncodeToString(imageBytes)
	return Part{
		Type:     "image_url",
		ImageURL: &ImageURL{URL: "data:" + contentType + ";base64," + encoded},
	}
}

// Message は1つの発言。
type Message struct {
	Role  string `json:"role"`
	Parts []Part `json:"content"`
}

// responseFormat は応答の形の指定。
//
// OpenAI 互換の口はこれを受け取ると、JSON として成り立つ出力しか
// 生成できないよう文法で縛る。Ollama・llama.cpp・LM Studio のいずれも解する。
type responseFormat struct {
	Type string `json:"type"`
}

// responseFormatJSONObject は「JSON オブジェクトだけを返せ」の指定。
const responseFormatJSONObject = "json_object"

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	// Temperature は判定の揺れを抑えるため0にする。
	Temperature float64 `json:"temperature"`
	Stream      bool    `json:"stream"`

	// MaxTokens は応答の長さの上限。maxAnswerTokens を参照。
	MaxTokens int `json:"max_tokens,omitempty"`

	// ResponseFormat は JSON だけを返させるための指定。
	//
	// **お願いするだけでは足りない。** 指示文でも JSON だけを求めているが、
	// 従わずに前置きを書いたり、途中で切れた JSON を返したりすることがある。
	// ここで指定すると文法で縛られるので、壊れた JSON が原理的に作れなくなる。
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete は問い合わせて応答の本文を返す。
func (c *Client) Complete(ctx context.Context, model string, messages []Message) (string, error) {
	if strings.TrimSpace(model) == "" {
		return "", errors.New("モデル名が指定されていません")
	}

	marshaled, err := json.Marshal(chatRequest{
		Model:          model,
		Messages:       messages,
		Temperature:    0,
		Stream:         false,
		MaxTokens:      maxAnswerTokens,
		ResponseFormat: &responseFormat{Type: responseFormatJSONObject},
	})
	if err != nil {
		return "", fmt.Errorf("error at marshal chat request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(marshaled))
	if err != nil {
		return "", fmt.Errorf("error at make chat request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		var netError net.Error
		if errors.As(err, &netError) && netError.Timeout() {
			return "", fmt.Errorf("%w (%s で打ち切りました): %w", ErrTimeout, c.httpClient.Timeout, err)
		}
		return "", fmt.Errorf("%w。起動しているか確認してください: %w", ErrUnreachable, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		peeked, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))

		// 文脈長が足りない場合は、何をすればよいかまで書く。
		// 写真は1枚で千数百トークンになるので、見本を数枚添えるだけで
		// 既定の 4096 を超える。素のエラー文だけでは原因が分からない。
		if strings.Contains(string(peeked), "context size") || strings.Contains(string(peeked), "n_ctx") {
			return "", fmt.Errorf(
				"%w: 文脈長が足りません。写真は1枚で千数百トークンになるため、"+
					"見本を添えると既定の 4096 では収まりません。\n"+
					"  次のどちらかで直せます:\n"+
					"    1. LLM 側の文脈長を広げる\n"+
					"       - Ollama       : 環境変数 OLLAMA_CONTEXT_LENGTH=16384 を設定して再起動\n"+
					"                        (モデル側に PARAMETER num_ctx を書く手もあります)\n"+
					"       - llama-server : 起動時の --ctx-size を大きくする\n"+
					"    2. 設定の candidates.max_few_shot_images を減らす (0 にすると見本を添えません)\n"+
					"  元の応答: %q", ErrRejected, string(peeked))
		}

		return "", fmt.Errorf("%w: HTTP %d: %q", ErrRejected, response.StatusCode, string(peeked))
	}

	decoded := chatResponse{}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return "", fmt.Errorf("%w: %w", ErrBadResponse, err)
	}
	if decoded.Error != nil {
		return "", fmt.Errorf("%w: %s", ErrRejected, decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("%w: 応答が空です", ErrBadResponse)
	}
	return decoded.Choices[0].Message.Content, nil
}

// modelsResponse は /v1/models の応答。
type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// ListModels は接続先で使えるモデルの名前を返す。
//
// OpenAI 互換の /v1/models を見る。Ollama・llama.cpp・LM Studio の
// いずれもこの口を持っているので、実装を問わず使える。
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.modelsEndpoint(), nil)
	if err != nil {
		return nil, fmt.Errorf("error at make models request: %w", err)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("LLM に繋がりません。起動しているか確認してください: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		peeked, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		return nil, fmt.Errorf("LLM が モデル一覧に HTTP %d を返しました: %q", response.StatusCode, string(peeked))
	}

	decoded := modelsResponse{}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("error at parse models response: %w", err)
	}

	names := make([]string, 0, len(decoded.Data))
	for _, entry := range decoded.Data {
		if entry.ID != "" {
			names = append(names, entry.ID)
		}
	}
	sort.Strings(names)
	return names, nil
}

// modelsEndpoint は設定された会話の口からモデル一覧の口を導く。
func (c *Client) modelsEndpoint() string {
	if trimmed, found := strings.CutSuffix(c.endpoint, "/chat/completions"); found {
		return trimmed + "/models"
	}
	// 想定と違う形の場合は、末尾を差し替えずに素直に足す。
	return strings.TrimRight(c.endpoint, "/") + "/models"
}

// visionMarkers は名前から視覚モデルらしさを見分ける手がかり。
//
// OpenAI 互換のモデル一覧は名前しか返さないので、名前で判断するしかない。
// 取りこぼしも誤判定もありうるため、選んだ結果は必ず利用者に見せて確かめてもらう。
var visionMarkers = []string{"vision", "llava", "minicpm-v", "vl"}

// LooksLikeVisionModel は名前から写真を扱えるモデルらしいかを返す。
func LooksLikeVisionModel(name string) bool {
	lowered := strings.ToLower(name)
	// タグ("qwen2.5vl:7b" の ":7b" 部分)を落として本体だけを見る。
	base, _, _ := strings.Cut(lowered, ":")

	for _, marker := range visionMarkers {
		switch marker {
		case "vl":
			// "vl" は短く紛れやすいので、語の切れ目に来る場合だけ拾う。
			if strings.HasSuffix(base, "vl") || strings.Contains(base, "vl-") || strings.Contains(base, "-vl") {
				return true
			}
		default:
			if strings.Contains(base, marker) {
				return true
			}
		}
	}
	return false
}

// ExtractJSONObject は応答から最初の JSON オブジェクトを取り出す。
//
// モデルは指示しても前置きや ```json の囲いを付けてくることがあるので、
// 素直に Unmarshal せず、対応する括弧の範囲を切り出す。
func ExtractJSONObject(text string) (string, bool) {
	start := strings.Index(text, "{")
	if start < 0 {
		return "", false
	}

	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(text); i++ {
		character := text[i]

		if escaped {
			escaped = false
			continue
		}
		if character == '\\' && inString {
			escaped = true
			continue
		}
		if character == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}

		switch character {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1], true
			}
		}
	}
	return "", false
}
