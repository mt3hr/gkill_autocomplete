// Package classify は LLM に候補タグの当否を尋ねる。
//
// suggest パッケージの Classifier を実装する。逐語一致や近傍の記録では
// 決まらなかった記録だけがここへ来る。
//
// LLM へ渡すのは「候補タグの名前」「対象の中身(本文または写真)」
// 「そのタグらしい記録の見本」の3つだけ。利用者のID・リポジトリ名・
// ファイル名・保存場所は渡さない。
package classify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillclient"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/llm"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/suggest"
)

const (
	// maxExamplesPerCandidate は候補1つあたりに見せる見本の数。
	maxExamplesPerCandidate = 2
	// maxTextLength は本文をこの長さで打ち切る。
	maxTextLength = 1500
	// defaultMaxExampleImages は見本写真の総数の既定。
	//
	// 写真は文字よりはるかに嵩む。1枚で千数百トークンになるので、
	// 文脈長の既定が 4096 のままの環境では2枚が上限に近い。
	defaultMaxExampleImages = 2

	// exampleThumbTimeout は見本写真1枚あたりの取得の期限。
	//
	// **見本は無くても判定はできる。** gkill 側の期限(既定120秒)を
	// そのまま待つと、取れない見本1枚のために記録1件あたり数分溶ける。
	// 判定する写真そのものは違うので、こちらの期限は掛けない。
	exampleThumbTimeout = 15 * time.Second
)

// ErrImageUnavailable は判定する写真を取れなかったことを表す。
//
// 呼び出し側が errors.Is で見分けて、記録の中身を含まない理由に置き換える。
var ErrImageUnavailable = errors.New("判定する写真を取得できません")

// ImageFetcher は写真を取ってくるもの。
type ImageFetcher interface {
	FetchThumb(ctx context.Context, repName string, fileName string, thumbSize string) (gkillclient.Image, error)
}

// Classifier は LLM を使う判定器。
type Classifier struct {
	llmClient *llm.Client
	images    ImageFetcher
	thumbSize string
	// maxExampleImages は1回の問い合わせで見せる見本写真の総数。
	maxExampleImages int
}

// New は判定器を作る。
//
// maxExampleImages が0以下のときは既定値を使う。
func New(llmClient *llm.Client, images ImageFetcher, thumbSize string, maxExampleImages int) *Classifier {
	if maxExampleImages <= 0 {
		maxExampleImages = defaultMaxExampleImages
	}
	return &Classifier{
		llmClient:        llmClient,
		images:           images,
		thumbSize:        thumbSize,
		maxExampleImages: maxExampleImages,
	}
}

// judgementsPayload は LLM に返させる形。
type judgementsPayload struct {
	Judgements []struct {
		Tag        string  `json:"tag"`
		Yes        bool    `json:"yes"`
		Confidence float64 `json:"confidence"`
		Reason     string  `json:"reason"`
	} `json:"judgements"`
}

// Classify は候補タグそれぞれについて当否を返す。
//
// 「どれが最も近いか」ではなく「それぞれ当てはまるか」を尋ねる。
// 記録には複数のタグが付くことも、1つも付かないこともあるため。
func (c *Classifier) Classify(ctx context.Context, record suggest.Record, candidates []suggest.Candidate) ([]suggest.Judgement, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	messages, model, err := c.buildMessages(ctx, record, candidates)
	if err != nil {
		return nil, err
	}
	if model == "" {
		// 使えるモデルが設定されていない。LLM の段階は飛ばす。
		return nil, nil
	}

	answer, err := c.llmClient.Complete(ctx, model, messages)
	if err != nil {
		return nil, err
	}

	return parseJudgements(answer, candidates)
}

// buildMessages は問い合わせの中身を組み立てる。
func (c *Classifier) buildMessages(ctx context.Context, record suggest.Record, candidates []suggest.Candidate) ([]llm.Message, string, error) {
	if record.IsImage && c.llmClient.VisionModel() != "" {
		messages, err := c.buildImageMessages(ctx, record, candidates)
		if err != nil {
			return nil, "", err
		}
		return messages, c.llmClient.VisionModel(), nil
	}

	if strings.TrimSpace(record.Text) != "" && c.llmClient.TextModel() != "" {
		return c.buildTextMessages(record, candidates), c.llmClient.TextModel(), nil
	}

	return nil, "", nil
}

// systemPrompt は判定の仕方を伝える。
//
// 「当てはまらない」を選びやすくしてあるのは、記録の一定割合が
// 意図的にタグ無しで残されるため。迷ったときに何かを選ばせると、
// 確認画面で却下する手間が増えるだけになる。
func systemPrompt() string {
	return strings.Join([]string{
		"あなたは生活の記録に付けるタグを判定します。",
		"候補として挙げたタグそれぞれについて、その記録に当てはまるかを個別に答えてください。",
		"",
		"守ること:",
		"- 候補は互いに排他ではありません。複数が当てはまることも、1つも当てはまらないこともあります。",
		"- 確信が持てないものは yes を false にしてください。当てずっぽうで選ばないでください。",
		"- 候補に無いタグを作らないでください。",
		"",
		"次の形の JSON だけを返してください。説明文は書かないでください。",
		`{"judgements":[{"tag":"候補の名前","yes":true,"confidence":0.0,"reason":"短い理由"}]}`,
	}, "\n")
}

func (c *Classifier) buildImageMessages(ctx context.Context, record suggest.Record, candidates []suggest.Candidate) ([]llm.Message, error) {
	parts := []llm.Part{llm.TextPart("これから、タグごとの見本の写真を見せます。")}

	shown := 0
	for _, candidate := range candidates {
		if shown >= c.maxExampleImages {
			break
		}
		for i, example := range candidate.ImageExamples {
			if i >= maxExamplesPerCandidate || shown >= c.maxExampleImages {
				break
			}
			image, err := c.fetchExampleThumb(ctx, example.RepName, example.FileName)
			if err != nil {
				// 見本が1枚取れなくても判定は続けられる。
				continue
			}
			parts = append(parts,
				llm.TextPart(fmt.Sprintf("これは「%s」の見本です。", candidate.Tag)),
				llm.ImagePart(image.Bytes, image.ContentType),
			)
			shown++
		}
	}

	target, err := c.images.FetchThumb(ctx, record.RepName, record.FileName, c.thumbSize)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrImageUnavailable, err)
	}

	parts = append(parts,
		llm.TextPart("ここからが判定してほしい写真です。"),
		llm.ImagePart(target.Bytes, target.ContentType),
		llm.TextPart(candidateInstruction(candidates)),
	)

	return []llm.Message{
		{Role: llm.RoleSystem, Parts: []llm.Part{llm.TextPart(systemPrompt())}},
		{Role: llm.RoleUser, Parts: parts},
	}, nil
}

// fetchExampleThumb は見本の写真を短い期限つきで取る。
//
// 取れなければ諦める。見本が欠けても判定はできるので、
// ここで長く待つ理由が無い。
func (c *Classifier) fetchExampleThumb(ctx context.Context, repName string, fileName string) (gkillclient.Image, error) {
	exampleCtx, cancel := context.WithTimeout(ctx, exampleThumbTimeout)
	defer cancel()
	return c.images.FetchThumb(exampleCtx, repName, fileName, c.thumbSize)
}

func (c *Classifier) buildTextMessages(record suggest.Record, candidates []suggest.Candidate) []llm.Message {
	builder := &strings.Builder{}

	builder.WriteString("タグごとの見本:\n")
	for _, candidate := range candidates {
		for i, example := range candidate.TextExamples {
			if i >= maxExamplesPerCandidate {
				break
			}
			fmt.Fprintf(builder, "- 「%s」の例: %s\n", candidate.Tag, truncate(example, 200))
		}
	}

	fmt.Fprintf(builder, "\n判定してほしい記録:\n%s\n", truncate(record.Text, maxTextLength))
	builder.WriteString("\n")
	builder.WriteString(candidateInstruction(candidates))

	return []llm.Message{
		{Role: llm.RoleSystem, Parts: []llm.Part{llm.TextPart(systemPrompt())}},
		{Role: llm.RoleUser, Parts: []llm.Part{llm.TextPart(builder.String())}},
	}
}

func candidateInstruction(candidates []suggest.Candidate) string {
	tagNames := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		tagNames = append(tagNames, candidate.Tag)
	}
	marshaled, err := json.Marshal(tagNames)
	if err != nil {
		// タグ名は単なる文字列なので、ここが失敗することはまず無い。
		marshaled = []byte("[]")
	}
	return fmt.Sprintf("候補のタグ: %s\nそれぞれについて当てはまるかを JSON で答えてください。", string(marshaled))
}

// parseJudgements は応答を解釈する。
//
// 候補に無いタグは捨てる。モデルが勝手に作った名前でタグを付けてしまわないため。
func parseJudgements(answer string, candidates []suggest.Candidate) ([]suggest.Judgement, error) {
	extracted, ok := llm.ExtractJSONObject(answer)
	if !ok {
		return nil, fmt.Errorf("%w: 応答から JSON を取り出せません", llm.ErrBadResponse)
	}

	payload := judgementsPayload{}
	if err := json.Unmarshal([]byte(extracted), &payload); err != nil {
		return nil, fmt.Errorf("%w: %w", llm.ErrBadResponse, err)
	}

	allowed := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		allowed[candidate.Tag] = struct{}{}
	}

	judgements := make([]suggest.Judgement, 0, len(payload.Judgements))
	for _, item := range payload.Judgements {
		if _, ok := allowed[item.Tag]; !ok {
			continue
		}
		judgements = append(judgements, suggest.Judgement{
			Tag:        item.Tag,
			Yes:        item.Yes,
			Confidence: item.Confidence,
			Reason:     truncate(item.Reason, 120),
		})
	}
	return judgements, nil
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
