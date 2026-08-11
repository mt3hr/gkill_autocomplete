package gkillclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/config"
)

// fakeSessionSource は gkillauth.SessionProvider の代わり。
//
// 本物は gkill の設定ディレクトリへ直接書くので、テストでは
// 「何回発行したか」「何回捨てたか」だけを数える。
type fakeSessionSource struct {
	userID string

	mu        atomic.Int32 // 発行した回数
	discarded atomic.Int32 // 捨てた回数

	current atomic.Value // string
}

func newFakeSessionSource() *fakeSessionSource {
	source := &fakeSessionSource{userID: "testuser"}
	source.current.Store("")
	return source
}

func (f *fakeSessionSource) UserID() string { return f.userID }

func (f *fakeSessionSource) SessionID(_ context.Context) (string, error) {
	if held, _ := f.current.Load().(string); held != "" {
		return held, nil
	}
	issued := f.mu.Add(1)
	sessionID := fmt.Sprintf("session-%d", issued)
	f.current.Store(sessionID)
	return sessionID, nil
}

func (f *fakeSessionSource) Invalidate(_ context.Context) {
	f.discarded.Add(1)
	f.current.Store("")
}

// issuedCount は発行した回数を返す。
func (f *fakeSessionSource) issuedCount() int32 { return f.mu.Load() }

// newTestClient はテスト用のサーバに向いたクライアントを作る。
func newTestClient(t *testing.T, handler http.Handler) (*Client, *fakeSessionSource) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	sessions := newFakeSessionSource()
	client, err := New(config.Default().Gkill, server.URL, sessions)
	if err != nil {
		t.Fatalf("クライアントを作れない: %v", err)
	}
	return client, sessions
}

// writeJSON はテスト用サーバのレスポンスを書く。
func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("レスポンスを書けない: %v", err)
	}
}

func TestSessionIsReusedAcrossCalls(t *testing.T) {
	// セッションは呼ぶたびに作り直さない。gkill の DB に行が溜まるうえ、
	// 発行のたびにファイルを開く必要も無い。
	client, sessions := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"errors": nil, "tag_names": []string{}})
	}))

	ctx := context.Background()
	for range 3 {
		if _, err := client.GetAllTagNames(ctx); err != nil {
			t.Fatalf("想定外のエラー: %v", err)
		}
	}

	if got := sessions.issuedCount(); got != 1 {
		t.Errorf("セッションの発行回数 = %d, want 1", got)
	}
}

func TestClientNeverCallsLoginAPI(t *testing.T) {
	// **gkill の /api/login は叩かない。**
	// 叩くと gkill 側のログイン回数(IP毎・15分に10回)を消費してしまい、
	// 総当たりを受けたときに利用者自身が gkill へ入れなくなる。
	loginCount := atomic.Int32{}

	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "login") {
			loginCount.Add(1)
		}
		writeJSON(t, w, map[string]any{"errors": nil, "tag_names": []string{}})
	}))

	if _, err := client.GetAllTagNames(context.Background()); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if got := loginCount.Load(); got != 0 {
		t.Errorf("/api/login を %d 回叩いた, want 0", got)
	}
}

func TestSessionExpiredTriggersReissue(t *testing.T) {
	// gkill 同梱の MCP 実装は ERR000373 を再取得の契機に含めておらず、
	// セッションが切れたあと取り直さずに落ちる。こちらでは必ず拾う。
	for _, errorCode := range []string{
		ErrCodeSessionExpired,
		ErrCodeSessionNotFound,
		ErrCodeAccountNotFound,
		ErrCodeAccountDisabled,
	} {
		t.Run(errorCode, func(t *testing.T) {
			callCount := atomic.Int32{}

			client, sessions := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// 1回目だけ認証エラーを返す。
				if callCount.Add(1) == 1 {
					writeJSON(t, w, map[string]any{
						"errors": []map[string]string{{"error_code": errorCode, "error_message": "expired"}},
					})
					return
				}
				writeJSON(t, w, map[string]any{"errors": nil, "tag_names": []string{"タグA"}})
			}))

			tagNames, err := client.GetAllTagNames(context.Background())
			if err != nil {
				t.Fatalf("取り直しで回復しなかった: %v", err)
			}
			if len(tagNames) != 1 {
				t.Errorf("タグ名 = %v, want 1件", tagNames)
			}
			if got := sessions.issuedCount(); got != 2 {
				t.Errorf("セッションの発行回数 = %d, want 2 (取り直していない)", got)
			}
		})
	}
}

func TestReissueHappensOnlyOnce(t *testing.T) {
	// 認証エラーが続く場合に取り直しを繰り返すと、
	// gkill の DB へセッションを書き続けることになる。
	client, sessions := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
			"errors": []map[string]string{{"error_code": ErrCodeSessionExpired, "error_message": "expired"}},
		})
	}))

	if _, err := client.GetAllTagNames(context.Background()); err == nil {
		t.Fatal("エラーを期待したが nil")
	}
	if got := sessions.issuedCount(); got != 2 {
		t.Errorf("セッションの発行回数 = %d, want 2 (取り直しは1回だけ)", got)
	}
}

func TestUserIDComesFromSessionSource(t *testing.T) {
	// 保存先を絞る鍵になるので、クライアントは自分が誰として動くかを答えられること。
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"errors": nil})
	}))

	if got := client.UserID(); got != "testuser" {
		t.Errorf("UserID() = %q, want %q", got, "testuser")
	}
}

func TestNewRequiresSessionSource(t *testing.T) {
	// セッションの供給元が無いと、誰として動くのか決まらない。
	if _, err := New(config.Default().Gkill, "http://127.0.0.1:9999", nil); err == nil {
		t.Fatal("セッションの供給元が無くてもクライアントが作れてしまった")
	}
}

func TestErrorsNullIsTreatedAsSuccess(t *testing.T) {
	// 成功時の errors は空配列ではなく null で返る。
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"messages":null,"errors":null,"tag_names":["タグA","タグB"]}`))
	}))

	tagNames, err := client.GetAllTagNames(context.Background())
	if err != nil {
		t.Fatalf("errors:null を失敗として扱ってしまった: %v", err)
	}
	if len(tagNames) != 2 {
		t.Errorf("タグ名 = %v, want 2件", tagNames)
	}
}

func TestAddTagAlreadyExistIsNotAnError(t *testing.T) {
	// 決定的なIDを使うので、再承認と「手で消したタグ」で必ず起きる。
	// どちらも「何もしなかった」が正しい結果。
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
			"errors": []map[string]string{{"error_code": ErrCodeAlreadyExistTag, "error_message": "already exist"}},
		})
	}))

	alreadyExist, err := client.AddTag(context.Background(), Tag{ID: "id-1", TargetID: "target-1", Tag: "タグA"})
	if err != nil {
		t.Fatalf("既存タグをエラーにしてしまった: %v", err)
	}
	if !alreadyExist {
		t.Error("alreadyExist = false, want true")
	}
}

func TestAddTagSendsRelatedTimeAndAppName(t *testing.T) {
	// related_time を省くとゼロ値(0001-01-01)になり時系列から外れる。
	// gkill 同梱の MCP 実装が踏んでいる穴。
	received := make(chan addTagRequest, 1)

	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := addTagRequest{}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("リクエストを解釈できない: %v", err)
		}
		received <- request
		writeJSON(t, w, map[string]any{"errors": nil, "added_tag": nil})
	}))

	relatedTime := time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC)
	now := time.Date(2020, 9, 9, 9, 9, 9, 0, time.UTC)
	tag := NewTag("tag-id", "target-id", "タグA", relatedTime, "testuser", "test-device", now)

	if _, err := client.AddTag(context.Background(), tag); err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}

	got := <-received
	if !got.Tag.RelatedTime.Equal(relatedTime) {
		t.Errorf("related_time = %v, want %v", got.Tag.RelatedTime, relatedTime)
	}
	if got.Tag.RelatedTime.IsZero() {
		t.Error("related_time がゼロ値になっている")
	}
	if got.Tag.CreateApp != AppName || got.Tag.UpdateApp != AppName {
		t.Errorf("create_app/update_app = %q/%q, want %q", got.Tag.CreateApp, got.Tag.UpdateApp, AppName)
	}
	if got.TXID != nil {
		t.Error("tx_id は nil であるべき(直接書き込み)")
	}
}

func TestForbiddenMentionsLocalOnlyAccess(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))

	_, err := client.GetAllTagNames(context.Background())
	if err == nil {
		t.Fatal("エラーを期待したが nil")
	}
	// 403 の原因はほぼローカル限定アクセスなので、そこへ誘導する。
	if !strings.Contains(err.Error(), "ローカル限定") {
		t.Errorf("原因の手がかりがエラーに無い: %v", err)
	}
}

func TestPlainHTTPAgainstTLSServerExplainsTheFix(t *testing.T) {
	// TLS で待っているサーバに平文で繋ぐと、Go の HTTP サーバは
	// ハンドシェイクの前に 400 とこの本文を返す。
	// 素のまま見せても何を直せばよいか分からないので、直し方まで書く。
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Client sent an HTTP request to an HTTPS server.\n"))
	}))

	_, err := client.GetAllTagNames(context.Background())
	if err == nil {
		t.Fatal("エラーを期待したが nil")
	}

	message := err.Error()
	// 空にすれば自動で合う、という一番簡単な直し方を必ず出す。
	for _, want := range []string{"base_url", "insecure_skip_verify"} {
		if !strings.Contains(message, want) {
			t.Errorf("%q が案内に含まれていない: %v", want, message)
		}
	}
}

func TestTLSAgainstPlainServerExplainsTheFix(t *testing.T) {
	// 逆に、平文で待っているサーバへ https:// で繋いだ場合。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	secureURL := strings.Replace(server.URL, "http://", "https://", 1)
	client, err := New(config.Default().Gkill, secureURL, newFakeSessionSource())
	if err != nil {
		t.Fatalf("クライアントを作れない: %v", err)
	}

	_, err = client.GetAllTagNames(context.Background())
	if err == nil {
		t.Fatal("エラーを期待したが nil")
	}
	if !strings.Contains(err.Error(), "平文") {
		t.Errorf("直し方の案内が無い: %v", err)
	}
}
