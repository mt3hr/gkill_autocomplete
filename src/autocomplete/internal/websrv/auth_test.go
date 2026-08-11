package websrv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	// 記録の中身に触れる口はすべて認証の後ろに置くこと。
	// 1つでも漏れると、そこから全部読めてしまう。
	server, openedStore := newTestServer(t, newFakeGkill(t, nil))
	putSuggestion(t, openedStore, "target-1", "タグA")

	cases := []struct {
		name    string
		request *http.Request
	}{
		{name: "提案の一覧", request: httptest.NewRequest(http.MethodPost, "/api/suggestions", strings.NewReader("{}"))},
		{name: "承認と却下", request: httptest.NewRequest(http.MethodPost, "/api/decide", strings.NewReader(`{"target_id":"target-1"}`))},
		{name: "解析の実行", request: httptest.NewRequest(http.MethodPost, "/api/analyze", strings.NewReader("{}"))},
		{name: "写真の中継", request: httptest.NewRequest(http.MethodGet, "/thumb?target=target-1", nil)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, testCase.request)
			if recorder.Code != http.StatusUnauthorized {
				t.Errorf("HTTP %d, want 401", recorder.Code)
			}
		})
	}
}

func TestUnauthenticatedRequestLeaksNothing(t *testing.T) {
	// 401 の本文に記録の中身が混ざらないこと。
	fake := newFakeGkill(t, []map[string]any{
		kyouJSON("target-1", map[string]any{"kind": "kmemo", "content": "秘密の本文"}),
	})
	server, openedStore := newTestServer(t, fake)
	putSuggestion(t, openedStore, "target-1", "秘密のタグ")

	recorder := doPostWithoutAuth(t, server, "/api/suggestions", map[string]any{})

	body := recorder.Body.String()
	for _, secret := range []string{"秘密の本文", "秘密のタグ", "target-1"} {
		if strings.Contains(body, secret) {
			t.Errorf("記録の中身が漏れている: %q in %q", secret, body)
		}
	}
}

func TestLoginWithCorrectCredentials(t *testing.T) {
	server, _ := newTestServer(t, newFakeGkill(t, nil))

	recorder := doPostWithoutAuth(t, server, "/api/login", loginRequest{
		UserID:         testUserID,
		PasswordSha256: credentialOf(testPassword),
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body.String())
	}

	found := false
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name != sessionCookieName {
			continue
		}
		found = true
		// 画面のスクリプトから読めないようにしておく。
		if !cookie.HttpOnly {
			t.Error("クッキーが HttpOnly でない")
		}
	}
	if !found {
		t.Error("セッションのクッキーが返らない")
	}
}

// gkill の全アカウントでログインできること。
//
// 解析の対象に入っていない人も、画面には入れる(提案が空になるだけ)。
func TestAnyEnabledAccountCanLogIn(t *testing.T) {
	server, _ := newTestServer(t, newFakeGkill(t, nil))

	recorder := doPostWithoutAuth(t, server, "/api/login", loginRequest{
		UserID:         otherUserID,
		PasswordSha256: credentialOf(otherPassword),
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("解析対象でないアカウントが入れない: HTTP %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestLoginRejectsWrongCredentials(t *testing.T) {
	// 弾く理由と順序は gkill の handle_login.go に合わせてある。
	cases := []struct {
		name     string
		userID   string
		password string
	}{
		{name: "パスワードが違う", userID: testUserID, password: "wrong"},
		{name: "利用者IDが違う", userID: "someone", password: testPassword},
		{name: "どちらも空", userID: "", password: ""},
		// 無効にされたアカウントは、パスワードが合っていても入れない。
		{name: "無効なアカウント", userID: disabledUserID, password: testPassword},
		// パスワード未設定は常に不一致(fail-closed)。空パスワードで入れない。
		{name: "パスワード未設定", userID: noPasswordUserID, password: ""},
		// リセットトークンが残っている間は、期限に関わらず入れない。
		{name: "パスワードリセット中", userID: resettingUserID, password: testPassword},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server, _ := newTestServer(t, newFakeGkill(t, nil))

			recorder := doPostWithoutAuth(t, server, "/api/login", loginRequest{
				UserID:         testCase.userID,
				PasswordSha256: credentialOf(testCase.password),
			})
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("HTTP %d, want 401", recorder.Code)
			}
			// どこで弾かれたかは言わない。言うとアカウントの存在や状態を探れる。
			body := recorder.Body.String()
			for _, leak := range []string{"無効", "リセット", "見つかりません"} {
				if strings.Contains(body, leak) {
					t.Errorf("弾いた理由が漏れている (%q): %q", leak, body)
				}
			}
		})
	}
}

func TestLoginAttemptsAreLimited(t *testing.T) {
	// ここを緩くすると、総当たりが gkill 側のログイン回数(15分に10回)を
	// 食い潰し、利用者が gkill そのものに入れなくなる。
	server, _ := newTestServer(t, newFakeGkill(t, nil))

	wrong := loginRequest{UserID: testUserID, PasswordSha256: credentialOf("wrong")}

	for i := range loginAttemptLimit {
		recorder := doPostWithoutAuth(t, server, "/api/login", wrong)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%d回目: HTTP %d, want 401", i+1, recorder.Code)
		}
	}

	recorder := doPostWithoutAuth(t, server, "/api/login", wrong)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("制限を超えた要求が HTTP %d, want 429", recorder.Code)
	}

	// 制限中は正しい資格情報でも通さない。
	recorder = doPostWithoutAuth(t, server, "/api/login", loginRequest{
		UserID:         testUserID,
		PasswordSha256: credentialOf(testPassword),
	})
	if recorder.Code != http.StatusTooManyRequests {
		t.Errorf("制限中に通ってしまった: HTTP %d", recorder.Code)
	}
	// gkill 側の回数は1つも使っていない。照合は手元の account.db に対して行うので、
	// ここが総当たりを受けても利用者が gkill から締め出されることはない。
}

func TestLogoutRevokesTheSession(t *testing.T) {
	server, openedStore := newTestServer(t, newFakeGkill(t, nil))
	putSuggestion(t, openedStore, "target-1", "タグA")

	cookie := loginCookie(t, server)

	// ログアウト前は通る。
	request := httptest.NewRequest(http.MethodPost, "/api/suggestions", strings.NewReader("{}"))
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("ログイン中なのに HTTP %d", recorder.Code)
	}

	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/logout", strings.NewReader("{}"))
	logoutRequest.AddCookie(cookie)
	logoutRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(logoutRecorder, logoutRequest)
	if logoutRecorder.Code != http.StatusOK {
		t.Fatalf("ログアウトできない: HTTP %d", logoutRecorder.Code)
	}

	// ログアウト後は同じクッキーでも通らない。
	afterRequest := httptest.NewRequest(http.MethodPost, "/api/suggestions", strings.NewReader("{}"))
	afterRequest.AddCookie(cookie)
	afterRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(afterRecorder, afterRequest)
	if afterRecorder.Code != http.StatusUnauthorized {
		t.Errorf("ログアウト後に通ってしまった: HTTP %d", afterRecorder.Code)
	}
}

func TestSessionEndpointReportsState(t *testing.T) {
	server, _ := newTestServer(t, newFakeGkill(t, nil))

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/session", nil))
	if !strings.Contains(recorder.Body.String(), "false") {
		t.Errorf("未ログインなのに認証済みと返った: %q", recorder.Body.String())
	}

	authenticated := doGet(t, server, "/api/session")
	if !strings.Contains(authenticated.Body.String(), "true") {
		t.Errorf("ログイン済みなのに未認証と返った: %q", authenticated.Body.String())
	}
}

// 他人の提案が見えないこと。
//
// **このアプリで一番まずい壊れ方。** gkill ではアカウントごとに
// 別のリポジトリを持つので、混ざると他人の記録の本文が画面に出る。
func TestLoggedInUserSeesOnlyTheirOwnSuggestions(t *testing.T) {
	fake := newFakeGkill(t, []map[string]any{
		kyouJSON("target-1", map[string]any{"kind": "kmemo", "content": "秘密の本文"}),
	})
	// 解析対象は testuser だけ。otheruser はログインできるが提案を持たない。
	server, openedStore := newTestServerForUsers(t, fake, testUserID)
	putSuggestionFor(t, openedStore, testUserID, "target-1", "秘密のタグ")

	request := httptest.NewRequest(http.MethodPost, "/api/suggestions", strings.NewReader("{}"))
	request.AddCookie(loginCookieAs(t, server, otherUserID, otherPassword))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, secret := range []string{"秘密の本文", "秘密のタグ", "target-1"} {
		if strings.Contains(body, secret) {
			t.Errorf("他人の記録が見えている: %q in %q", secret, body)
		}
	}
}

// 解析対象でない利用者は、承認も解析もできないこと。
func TestNonAnalyzedUserCannotDecideOrAnalyze(t *testing.T) {
	fake := newFakeGkill(t, nil)
	server, openedStore := newTestServerForUsers(t, fake, testUserID)
	putSuggestionFor(t, openedStore, testUserID, "target-1", "タグA")

	cookie := loginCookieAs(t, server, otherUserID, otherPassword)

	for _, path := range []string{"/api/decide", "/api/analyze"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, path,
				strings.NewReader(`{"target_id":"target-1","approve_tags":["タグA"]}`))
			request.AddCookie(cookie)
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, request)

			if recorder.Code != http.StatusForbidden {
				t.Errorf("HTTP %d, want 403", recorder.Code)
			}
		})
	}

	// 他人の提案が判定されていないこと。
	pending, err := openedStore.CountPending(context.Background(), testUserID)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if pending != 1 {
		t.Errorf("他人に判定された: 確認待ち = %d件, want 1件", pending)
	}
}

// 写真の中継も利用者ごとに分かれていること。
//
// 索引を共有していると、記録IDさえ知っていれば他人の一覧に載った写真を引ける。
func TestThumbIsIsolatedPerUser(t *testing.T) {
	fake := newFakeGkill(t, []map[string]any{
		kyouJSON("target-1", map[string]any{
			"kind": "idf", "file_name": "a.jpg", "is_image": true,
			"rep_name": "SampleRep_DeviceA_20200101",
		}),
	})
	server, openedStore := newTestServerForUsers(t, fake, testUserID)
	putSuggestionFor(t, openedStore, testUserID, "target-1", "タグA")

	// testuser が一覧を見て索引を作る。
	doPost(t, server, "/api/suggestions", map[string]any{})

	// otheruser が同じ記録IDで中継を頼んでも通らない。
	request := httptest.NewRequest(http.MethodGet, "/thumb?target=target-1", nil)
	request.AddCookie(loginCookieAs(t, server, otherUserID, otherPassword))
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("他人の写真が引けてしまった: HTTP %d", recorder.Code)
	}
}

// セッションの応答が、その人が解析対象かどうかを伝えること。
//
// 伝えないと、提案が0件なのか対象外なのかを画面が出し分けられない。
func TestSessionReportsWhetherUserIsAnalyzed(t *testing.T) {
	server, _ := newTestServerForUsers(t, newFakeGkill(t, nil), testUserID)

	mine := doGet(t, server, "/api/session")
	if !strings.Contains(mine.Body.String(), `"analyzable":true`) {
		t.Errorf("解析対象なのにそう返らない: %q", mine.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	request.AddCookie(loginCookieAs(t, server, otherUserID, otherPassword))
	theirs := httptest.NewRecorder()
	server.Handler().ServeHTTP(theirs, request)

	if !strings.Contains(theirs.Body.String(), `"analyzable":false`) {
		t.Errorf("解析対象でないのにそう返らない: %q", theirs.Body.String())
	}
}
