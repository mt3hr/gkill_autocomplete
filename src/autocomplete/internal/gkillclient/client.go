package gkillclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/config"
)

// AppName は gkill に書き込む記録の CREATE_APP / UPDATE_APP に刻む名前。
// 後からこのツールが付けたタグを見分けられるようにするためのもの。
const AppName = "gkill_autocomplete"

// maxResponseBytes は非2xxのときに本文を読む上限。
// gkill 形式なら業務エラーとして返すので、errors 配列が入る程度には要る。
const maxResponseBytes = 1 * 1024 * 1024

// looksLikeGkillEnvelope は本文が gkill の応答形式(errors / messages を持つ JSON)かを返す。
//
// ステータスが 2xx でなくても、この形なら業務エラーとして呼び出し側へ渡す。
// 呼び出し側は decodeEnvelope で errors を読み、今までどおり error_code で判断する。
func looksLikeGkillEnvelope(body []byte) bool {
	var probe struct {
		Errors   json.RawMessage `json:"errors"`
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Errors != nil || probe.Messages != nil
}

// maxErrorBodyBytes はエラー時にレスポンス本文を読む上限。
// 記録の本文が丸ごとエラーメッセージに乗ってログへ流れるのを防ぐ。
const maxErrorBodyBytes = 512

// SessionSource は gkill を叩くためのセッションを供給する。
//
// 実体は internal/gkillauth の SessionProvider で、gkill の設定ディレクトリへ
// 直接書いてセッションを発行する。**パスワードは要らない**。
// ここをインターフェースにしてあるのは、この層が
// 「どうやってセッションを手に入れるか」を知らずに済ませるため。
type SessionSource interface {
	// UserID はそのセッションが動く利用者を返す。
	UserID() string

	// SessionID は有効なセッションIDを返す。期限が近ければ発行し直す。
	SessionID(ctx context.Context) (string, error)

	// Invalidate は手元のセッションを捨てる。次の SessionID で作り直す。
	Invalidate(ctx context.Context)
}

// Client は稼働中の gkill サーバへの HTTP クライアント。
//
// gkill 本体には一切変更を加えず、公開されている API だけを使う。
type Client struct {
	baseURL    string
	localeName string
	httpClient *http.Client
	sessions   SessionSource
}

// New はクライアントを作る。
//
// baseURL は gkill 本体の宛先。設定が空のときは呼び出し側が
// server_config.db から組み立てたものを渡す。
func New(gkillConfig config.GkillConfig, baseURL string, sessions SessionSource) (*Client, error) {
	resolvedBaseURL := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if resolvedBaseURL == "" {
		return nil, errors.New("gkill の接続先が決まりません")
	}
	if sessions == nil {
		return nil, errors.New("セッションの供給元がありません")
	}

	transport := http.DefaultTransport
	if gkillConfig.InsecureSkipVerify {
		// ローカルの gkill が自己署名証明書を使っている場合向け。
		cloned := http.DefaultTransport.(*http.Transport).Clone()
		cloned.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // localhost向けの自己署名証明書のため
		transport = cloned
	}

	return &Client{
		baseURL:    resolvedBaseURL,
		localeName: gkillConfig.LocaleName,
		httpClient: &http.Client{
			Timeout:   gkillConfig.Timeout(),
			Transport: transport,
		},
		sessions: sessions,
	}, nil
}

// UserID はこのクライアントが動く利用者を返す。
func (c *Client) UserID() string {
	return c.sessions.UserID()
}

// BaseURL は接続先を返す。
func (c *Client) BaseURL() string {
	return c.baseURL
}

// LocaleName はリクエストに載せるロケールを返す。
func (c *Client) LocaleName() string {
	return c.localeName
}

// EnsureSession はセッションを用意して返す。
//
// **gkill の /api/login は叩かない。** 設定ディレクトリへ直接書いて発行するので、
// gkill 側のログイン回数(IP毎・15分に10回)を一切消費しない。
// 資格情報も要らないため、コマンドラインにパスワードを渡さずに済む。
func (c *Client) EnsureSession(ctx context.Context) (string, error) {
	sessionID, err := c.sessions.SessionID(ctx)
	if err != nil {
		return "", fmt.Errorf("gkill のセッションを用意できませんでした: %w", err)
	}
	return sessionID, nil
}

// callAuthed はセッション付きの POST を行い、レスポンス本文を返す。
//
// 認証エラーだったときだけ1回再ログインして送り直す。
// ネットワークエラーやタイムアウトの再試行はしない。
//
// build は session_id を受け取ってリクエスト本体を組み立てる。
// 再送時に新しい session_id で組み直せるよう関数で受ける。
func (c *Client) callAuthed(ctx context.Context, path string, build func(sessionID string) any) ([]byte, error) {
	sessionID, err := c.EnsureSession(ctx)
	if err != nil {
		return nil, err
	}

	raw, err := c.postRaw(ctx, path, build(sessionID))
	if err != nil {
		return nil, err
	}

	if hasAuthError(decodeEnvelope(raw).Errors) {
		// セッションが死んでいる。捨てて取り直し、1回だけ送り直す。
		c.sessions.Invalidate(ctx)

		sessionID, err = c.EnsureSession(ctx)
		if err != nil {
			return nil, err
		}
		raw, err = c.postRaw(ctx, path, build(sessionID))
		if err != nil {
			return nil, err
		}
	}

	if err := asAPIError(path, decodeEnvelope(raw).Errors); err != nil {
		return nil, err
	}
	return raw, nil
}

// wrapTransportError は接続そのものが失敗したときの説明を足す。
//
// 接続先の書き方の取り違えは、素のエラー文だけでは何を直せばよいか分かりにくい。
// 直し方まで書く。
func (c *Client) wrapTransportError(path string, err error) error {
	message := err.Error()

	// 平文で待っているサーバに TLS で繋いだ場合。
	if strings.Contains(message, "server gave HTTP response to HTTPS client") {
		return fmt.Errorf(
			"gkill は平文(http)で待ち受けていますが、設定の gkill.base_url が https:// になっています。\n"+
				"  現在の設定: %s\n"+
				"  gkill.base_url を空にすれば、gkill 自身のサーバ設定に合わせます。\n"+
				"  手で指定する場合は次のように直してください:\n"+
				"    \"base_url\": \"%s\"",
			c.baseURL, strings.Replace(c.baseURL, "https://", "http://", 1))
	}

	// 自己署名の証明書を検証しようとした場合。
	if strings.Contains(message, "x509:") || strings.Contains(message, "certificate") {
		return fmt.Errorf(
			"gkill の証明書を検証できませんでした。\n"+
				"  gkill が localhost 向けに使う証明書は自己署名なので、次を設定してください:\n"+
				"    \"insecure_skip_verify\": true\n"+
				"  (環境変数 GKILL_INSECURE=1 でも同じです)\n"+
				"  元のエラー: %w", err)
	}

	if strings.Contains(message, "connection refused") || strings.Contains(message, "actively refused") {
		return fmt.Errorf(
			"gkill に繋がりません (%s)。動いているか、接続先が合っているかを確かめてください。\n"+
				"  元のエラー: %w", c.baseURL, err)
	}

	return fmt.Errorf("error at request %s: %w", path, err)
}

// decodeEnvelope はレスポンスの共通部分だけを取り出す。
// 呼ぶたびに新しい値へ読み込むので、再送の前後で結果が混ざらない。
func decodeEnvelope(raw []byte) responseEnvelope {
	envelope := responseEnvelope{}
	// 解釈できない場合はエラー無しとして扱う。本体の Unmarshal 側が報告する。
	_ = json.Unmarshal(raw, &envelope)
	return envelope
}

// postRaw は JSON を POST してレスポンス本文を返す。
func (c *Client) postRaw(ctx context.Context, path string, body any) ([]byte, error) {
	marshaled, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("error at marshal request for %s: %w", path, err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(marshaled))
	if err != nil {
		return nil, fmt.Errorf("error at make request for %s: %w", path, err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, c.wrapTransportError(path, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		// **gkill 形式の本文なら、ステータスに関わらず呼び出し側へ返す。**
		// gkill は 2026-08 から異常時に 4xx/5xx を返すが(ADR-0045)、
		// エラーの中身(error_code)は今までどおり本文の errors にしか入っていない。
		// ここで打ち切ると callAuthed の hasAuthError に到達せず、
		// **セッションの取り直しが一度も走らなくなる**。
		// AddTag の「既存タグは成功扱い」(IsAlreadyExistTag)も同じ理由で効かなくなる。
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
		if readErr == nil && looksLikeGkillEnvelope(body) {
			return body, nil
		}

		// 本文には記録の中身が入りうるので、エラー文へ載せる量は絞る。
		peeked := body
		if len(peeked) > maxErrorBodyBytes {
			peeked = peeked[:maxErrorBodyBytes]
		}

		// TLS で待っているサーバに平文で繋いだ場合。接続先の書き方を直せば済む。
		if strings.Contains(string(peeked), "HTTP request to an HTTPS server") {
			return nil, fmt.Errorf(
				"gkill は TLS で待ち受けていますが、接続先が平文(http://)になっています。\n"+
					"  現在の接続先: %s\n"+
					"\n"+
					"  接続先は通常 gkill 自身のサーバ設定から決まります。\n"+
					"  設定ファイルの gkill.base_url を空にすれば、そちらに合わせます:\n"+
					"    \"base_url\": \"\"\n"+
					"\n"+
					"  手で指定したい場合は https:// にした上で、次も併せて設定してください:\n"+
					"    \"insecure_skip_verify\": true\n"+
					"  (gkill が localhost 向けに使う証明書は自己署名なので、検証を通せません)",
				c.baseURL)
		}

		if response.StatusCode == http.StatusForbidden {
			// gkill は 403 を認可の拒否全般(管理者権限なし・アカウント無効)にも使うようになった。
			// ただしその場合は本文が gkill 形式なので上で返っている。
			// ここへ来るのは本文が読めない 403 —— つまりローカル限定アクセスで
			// 前段のフィルタに弾かれた場合がほとんど。
			return nil, fmt.Errorf(
				"gkill が %s へのアクセスを拒否しました (HTTP 403)。"+
					"gkill 側でローカル限定アクセスが有効な場合、同じ端末からでないと通りません", path)
		}
		return nil, fmt.Errorf("gkill が %s に HTTP %d を返しました: %q", path, response.StatusCode, string(peeked))
	}

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("error at read response of %s: %w", path, err)
	}
	return raw, nil
}
