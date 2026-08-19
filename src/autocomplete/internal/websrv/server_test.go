package websrv

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/app"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/config"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillclient"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/ids"
	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/store"
)

func at(hour int) time.Time {
	return time.Date(2020, 6, 1, hour, 0, 0, 0, time.UTC)
}

// fakeGkill は gkill を模したサーバ。addedTags に書き込まれたタグが入る。
type fakeGkill struct {
	server    *httptest.Server
	addedTags chan gkillclient.Tag
	kyous     []map[string]any
	imageHits atomic.Int32

	// onGetKyous は /api/get_kyous_mcp の応答を要求に応じて組み立てる差込口。
	// nil なら kyous をそのまま返す。
	onGetKyous func(requestedIDs []string) []map[string]any
}

func newFakeGkill(t *testing.T, kyous []map[string]any) *fakeGkill {
	t.Helper()

	fake := &fakeGkill{addedTags: make(chan gkillclient.Tag, 16), kyous: kyous}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/files/") {
			fake.imageHits.Add(1)
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": nil, "session_id": "session-1"})
		case "/api/get_kyous_mcp":
			kyous := fake.kyous
			if fake.onGetKyous != nil {
				request := struct {
					Query struct {
						IDs []string `json:"ids"`
					} `json:"query"`
				}{}
				_ = json.NewDecoder(r.Body).Decode(&request)
				kyous = fake.onGetKyous(request.Query.IDs)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": nil, "kyous": kyous, "has_more": false})
		case "/api/add_tag":
			request := struct {
				Tag gkillclient.Tag `json:"tag"`
			}{}
			_ = json.NewDecoder(r.Body).Decode(&request)
			fake.addedTags <- request.Tag
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": nil, "added_tag": request.Tag})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": nil})
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

// discardLogger は何も出さないログ。テストの出力を汚さないため。
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// fixedSessionSource は gkillauth.SessionProvider の代わり。
//
// 本物は gkill の設定ディレクトリへ直接書くので、テストでは固定値を返す。
type fixedSessionSource struct{ userID string }

func (s fixedSessionSource) UserID() string { return s.userID }

func (s fixedSessionSource) SessionID(_ context.Context) (string, error) { return "session-1", nil }

func (s fixedSessionSource) Invalidate(_ context.Context) {}

func newTestServer(t *testing.T, fake *fakeGkill) (*Server, *store.Store) {
	t.Helper()
	return newTestServerForUsers(t, fake, testUserID)
}

// newTestServerForUsers は指定した利用者ぶんの App を持つサーバを作る。
//
// ログインできるのはアカウントDBにいる全員だが、提案を持つのはここに渡した人だけ。
func newTestServerForUsers(t *testing.T, fake *fakeGkill, userIDs ...string) (*Server, *store.Store) {
	t.Helper()

	appConfig := config.Default()
	appConfig.Gkill.BaseURL = fake.server.URL

	home := t.TempDir()
	openedStore, err := store.Open(context.Background(), filepath.Join(home, "test.db"))
	if err != nil {
		t.Fatalf("保存先を開けない: %v", err)
	}
	t.Cleanup(func() { _ = openedStore.Close() })

	apps := make([]*app.App, 0, len(userIDs))
	for _, userID := range userIDs {
		client, err := gkillclient.New(appConfig.Gkill, fake.server.URL, fixedSessionSource{userID: userID})
		if err != nil {
			t.Fatalf("クライアントを作れない: %v", err)
		}
		apps = append(apps, &app.App{
			Config: appConfig,
			Client: client,
			Store:  openedStore,
			Logger: discardLogger(),
		})
	}

	frontend := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>確認画面</html>")}}
	return New(apps, newTestVerifier(t), frontend, discardLogger()), openedStore
}

func putSuggestion(t *testing.T, openedStore *store.Store, targetID string, tagName string) store.Suggestion {
	t.Helper()
	return putSuggestionFor(t, openedStore, testUserID, targetID, tagName)
}

func putSuggestionFor(t *testing.T, openedStore *store.Store, userID string, targetID string, tagName string) store.Suggestion {
	t.Helper()

	suggestion := store.Suggestion{
		ID:          ids.SuggestionID(targetID, tagName),
		TagID:       ids.TagID(targetID, tagName),
		TargetID:    targetID,
		Tag:         tagName,
		Confidence:  0.9,
		Tier:        "text_match",
		Reason:      "同じ本文の記録に付いていたタグ",
		DataType:    "kmemo",
		RelatedTime: at(8),
		SuggestedAt: at(9),
	}
	if _, err := openedStore.PutSuggestion(context.Background(), userID, suggestion); err != nil {
		t.Fatalf("提案を保存できない: %v", err)
	}
	return suggestion
}

func kyouJSON(id string, payload map[string]any) map[string]any {
	return map[string]any{
		"id":           id,
		"data_type":    "kmemo",
		"related_time": at(8).Format(time.RFC3339),
		"payload":      payload,
	}
}

// loginCookie は確認画面へログインして、以後の要求に付けるクッキーを返す。
func loginCookie(t *testing.T, server *Server) *http.Cookie {
	t.Helper()
	return loginCookieAs(t, server, testUserID, testPassword)
}

func loginCookieAs(t *testing.T, server *Server, userID string, password string) *http.Cookie {
	t.Helper()

	recorder := doPostWithoutAuth(t, server, "/api/login", loginRequest{
		UserID:         userID,
		PasswordSha256: credentialOf(password),
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("ログインできない: HTTP %d %s", recorder.Code, recorder.Body.String())
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == sessionCookieName {
			return cookie
		}
	}
	t.Fatal("ログインしたのにクッキーが返らない")
	return nil
}

func doPostWithoutAuth(t *testing.T, server *Server, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	marshaled, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("リクエストを組み立てられない: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(marshaled)))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func doPost(t *testing.T, server *Server, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	marshaled, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("リクエストを組み立てられない: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(marshaled)))
	request.AddCookie(loginCookie(t, server))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

// doGet は認証つきの GET を行う。
func doGet(t *testing.T, server *Server, path string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(loginCookie(t, server))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestServesEmbeddedFrontend(t *testing.T) {
	server, _ := newTestServer(t, newFakeGkill(t, nil))

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "確認画面") {
		t.Errorf("画面が配信されていない: %q", recorder.Body.String())
	}
}

func TestSuggestionsGroupsByRecord(t *testing.T) {
	fake := newFakeGkill(t, []map[string]any{
		kyouJSON("target-1", map[string]any{"kind": "kmemo", "content": "本文"}),
	})
	server, openedStore := newTestServer(t, fake)

	putSuggestion(t, openedStore, "target-1", "タグA")
	putSuggestion(t, openedStore, "target-1", "タグB")

	recorder := doPost(t, server, "/api/suggestions", map[string]any{})
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body.String())
	}

	response := suggestionsResponse{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("応答を解釈できない: %v", err)
	}
	if len(response.Records) != 1 {
		t.Fatalf("記録 = %d件, want 1件", len(response.Records))
	}
	// 1つの記録に複数の候補が並ぶ。どれを選ぶかは人が決める。
	if len(response.Records[0].Suggestions) != 2 {
		t.Errorf("候補 = %d件, want 2件", len(response.Records[0].Suggestions))
	}
	if response.Pending != 2 {
		t.Errorf("確認待ち = %d件, want 2件", response.Pending)
	}
}

func TestSuggestionsSkipsRecordsGoneFromGkill(t *testing.T) {
	// gkill 側で消された記録は画面に出さない。
	fake := newFakeGkill(t, []map[string]any{})
	server, openedStore := newTestServer(t, fake)

	putSuggestion(t, openedStore, "target-gone", "タグA")

	recorder := doPost(t, server, "/api/suggestions", map[string]any{})
	response := suggestionsResponse{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("応答を解釈できない: %v", err)
	}
	if len(response.Records) != 0 {
		t.Errorf("消えた記録が出ている: %+v", response.Records)
	}
}

func TestDecideApprovesSelectedTagOnly(t *testing.T) {
	fake := newFakeGkill(t, []map[string]any{
		kyouJSON("target-1", map[string]any{"kind": "kmemo", "content": "本文"}),
	})
	server, openedStore := newTestServer(t, fake)

	putSuggestion(t, openedStore, "target-1", "タグA")
	putSuggestion(t, openedStore, "target-1", "タグB")

	recorder := doPost(t, server, "/api/decide", decideRequest{
		TargetID:    "target-1",
		ApproveTags: []string{"タグA"},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body.String())
	}

	response := decideResponse{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("応答を解釈できない: %v", err)
	}
	if response.Approved != 1 || response.Rejected != 1 {
		t.Errorf("承認 %d件 / 却下 %d件, want 1件 / 1件", response.Approved, response.Rejected)
	}

	// gkill へ書き込まれたのは選んだタグだけ。
	select {
	case tag := <-fake.addedTags:
		if tag.Tag != "タグA" {
			t.Errorf("書き込まれたタグ = %q, want タグA", tag.Tag)
		}
		if !tag.RelatedTime.Equal(at(8)) {
			t.Errorf("related_time = %v, want %v (記録の時刻に合わせる)", tag.RelatedTime, at(8))
		}
		if tag.CreateApp != gkillclient.AppName {
			t.Errorf("create_app = %q", tag.CreateApp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("タグが書き込まれなかった")
	}

	// 選ばなかったほうは書き込まれない。
	select {
	case tag := <-fake.addedTags:
		t.Errorf("選んでいないタグが書き込まれた: %q", tag.Tag)
	default:
	}

	// どちらも確認待ちから消える。
	pending, err := openedStore.CountPending(context.Background(), testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if pending != 0 {
		t.Errorf("確認待ち = %d件, want 0件", pending)
	}
}

func TestDecideWithNoTagsRecordsNoTagNeeded(t *testing.T) {
	// 記録の一定割合は意図的にタグを付けない。
	// 一度そう決めたら二度と提案しないこと。
	fake := newFakeGkill(t, []map[string]any{
		kyouJSON("target-1", map[string]any{"kind": "kmemo", "content": "本文"}),
	})
	server, openedStore := newTestServer(t, fake)

	putSuggestion(t, openedStore, "target-1", "タグA")

	recorder := doPost(t, server, "/api/decide", decideRequest{
		TargetID:    "target-1",
		ApproveTags: []string{},
		NoTagNeeded: true,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body.String())
	}

	select {
	case tag := <-fake.addedTags:
		t.Errorf("タグ不要にしたのに書き込まれた: %q", tag.Tag)
	default:
	}

	// 解析をやり直しても提案が復活しないこと。
	ctx := context.Background()
	stored, err := openedStore.PutSuggestion(ctx, testUserID, store.Suggestion{
		ID:       ids.SuggestionID("target-1", "タグC"),
		TagID:    ids.TagID("target-1", "タグC"),
		TargetID: "target-1",
		Tag:      "タグC",
	})
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if stored {
		t.Error("タグ不要にした記録に新しい提案が入った")
	}
}

func TestDecideRequiresTargetID(t *testing.T) {
	server, _ := newTestServer(t, newFakeGkill(t, nil))

	recorder := doPost(t, server, "/api/decide", map[string]any{})
	if recorder.Code == http.StatusOK {
		t.Error("target_id が無いのに通ってしまった")
	}
}

func TestThumbOnlyServesRecordsFromLastListing(t *testing.T) {
	// 画像の中継口はリポジトリ名やファイル名を外から受け取らない。
	// 一覧に出ていない場所のファイルを読ませないため。
	fake := newFakeGkill(t, []map[string]any{
		kyouJSON("target-1", map[string]any{
			"kind": "idf", "file_name": "a.jpg", "is_image": true,
			"rep_name": "SampleRep_DeviceA_20200101",
		}),
	})
	server, openedStore := newTestServer(t, fake)
	putSuggestion(t, openedStore, "target-1", "タグA")

	// 一覧を見る前は中継しない。
	recorder := doGet(t, server, "/thumb?target=target-1")
	if recorder.Code != http.StatusNotFound {
		t.Errorf("一覧前の中継 = HTTP %d, want 404", recorder.Code)
	}

	// 一覧を見たあとは中継する。
	doPost(t, server, "/api/suggestions", map[string]any{})

	recorder = doGet(t, server, "/thumb?target=target-1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("中継 = HTTP %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Error("画像がキャッシュされうる設定になっている")
	}
	if fake.imageHits.Load() != 1 {
		t.Errorf("gkill への画像取得 = %d回, want 1回", fake.imageHits.Load())
	}

	// 一覧に無い記録は中継しない。
	recorder = doGet(t, server, "/thumb?target=other")
	if recorder.Code != http.StatusNotFound {
		t.Errorf("一覧に無い記録の中継 = HTTP %d, want 404", recorder.Code)
	}
}

func TestThumbRequiresTarget(t *testing.T) {
	server, _ := newTestServer(t, newFakeGkill(t, nil))

	recorder := doGet(t, server, "/thumb")

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("HTTP %d, want 400", recorder.Code)
	}
}

func TestAnalyzeEndpointReturnsReport(t *testing.T) {
	fake := newFakeGkill(t, []map[string]any{
		kyouJSON("past-1", map[string]any{"kind": "kmemo", "content": "定型の本文"}),
	})
	server, _ := newTestServer(t, fake)

	recorder := doPost(t, server, "/api/analyze", map[string]any{})
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body.String())
	}

	// 解析はリクエストとは別の寿命で走る。終わるのを待ってから中身を見る。
	response := waitAnalyzeFinished(t, server)
	if response.Failure != "" {
		t.Fatalf("解析が落ちた: %s", response.Failure)
	}
	if response.Report == nil {
		t.Fatal("解析の結果が返らない")
	}
	if response.Report.LearnedRecords != 1 {
		t.Errorf("学習した記録 = %d件, want 1件", response.Report.LearnedRecords)
	}
}

// TestAnalyzeKeepsRunningAfterRequestIsCancelled は、要求が切れても
// 解析が続くことを確かめる。
//
// **これを落とすと、タブを閉じただけで解析が止まる作りに戻る。**
// 写真の判定は1件で数分かかるので、1本の要求の寿命に縛ってはいけない。
func TestAnalyzeKeepsRunningAfterRequestIsCancelled(t *testing.T) {
	fake := newFakeGkill(t, []map[string]any{
		kyouJSON("past-1", map[string]any{"kind": "kmemo", "content": "定型の本文"}),
	})
	server, _ := newTestServer(t, fake)

	// 途中で切れる文脈で解析を頼む。
	cancelledCtx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/api/analyze", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(loginCookie(t, server))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request.WithContext(cancelledCtx))
	cancel()

	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body.String())
	}

	response := waitAnalyzeFinished(t, server)
	if response.Failure != "" {
		t.Fatalf("要求が切れたせいで解析が落ちている: %s", response.Failure)
	}
	if response.Report == nil {
		t.Fatal("解析の結果が返らない")
	}
}

// waitAnalyzeFinished は解析が終わるまで状態を見に行く。
func waitAnalyzeFinished(t *testing.T, server *Server) analyzeStatusResponse {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		recorder := doGet(t, server, "/api/analyze/status")
		if recorder.Code != http.StatusOK {
			t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body.String())
		}

		response := analyzeStatusResponse{}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("応答を解釈できない: %v", err)
		}
		if !response.Running && (response.Report != nil || response.Failure != "") {
			return response
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal("解析が終わらない")
	return analyzeStatusResponse{}
}
