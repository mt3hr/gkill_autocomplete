package websrv

// 編集前に読む: .claude/skills/autocomplete-gkill-auth/SKILL.md（この領域の不変条件の正本）

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillauth"
)

const (
	// sessionCookieName は確認画面のログイン状態を持つクッキー。
	sessionCookieName = "gkill_autocomplete_session"

	// sessionTTL はログイン状態の有効期間。
	//
	// 確認画面は何日も上げたままにするので長く取る。
	// **これはログインした端末に1週間ぶんの鍵が残るということ。**
	// 画面に出るのは記録の本文と写真そのものなので、
	// 共用の端末で開いたときは必ずログアウトすること。
	sessionTTL = 7 * 24 * time.Hour

	// loginAttemptLimit と loginAttemptWindow はログイン試行の制限。
	//
	// gkill 本体の制限(15分に10回)より厳しくしてある。
	// なお照合は手元の account.db に対して行うので、ここが総当たりを受けても
	// gkill 側の回数は減らない。利用者が gkill から締め出されることはない。
	loginAttemptLimit  = 5
	loginAttemptWindow = 15 * time.Minute
)

// credentialPattern は資格情報の書式(SHA-256 の小文字16進64桁)。
//
// gkill と同じく、**ログインの可否をこれで決めない**。書式で早期に弾くと、
// 応答の速さや文面から書式の正誤が読み取れてしまう。
// 画面が壊れた値を送っていないかを記録に残すためだけに使う。
var credentialPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// session は1つのログイン状態。
type session struct {
	userID    string
	expiresAt time.Time
}

// authenticator は確認画面へのログインを管理する。
//
// 照合は gkill のアカウントDB(account.db)に対して行う。
// gkill の /api/login は叩かない。叩くと gkill 側のログイン回数
// (IP毎・15分に10回)を消費してしまい、総当たりを受けたときに
// 利用者自身が gkill へログインできなくなるため。
type authenticator struct {
	verifier *gkillauth.Verifier
	logger   *slog.Logger

	mu       sync.Mutex
	sessions map[string]session
	attempts map[string][]time.Time
}

func newAuthenticator(verifier *gkillauth.Verifier, logger *slog.Logger) *authenticator {
	return &authenticator{
		verifier: verifier,
		logger:   logger,
		sessions: map[string]session{},
		attempts: map[string][]time.Time{},
	}
}

// issue は新しいログイン状態を作る。
func (a *authenticator) issue(userID string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions[token] = session{userID: userID, expiresAt: time.Now().Add(sessionTTL)}
	return token, nil
}

// userOf はログイン状態が有効なら、その利用者IDを返す。
//
// **画面に出すものはすべてこの戻り値で絞る。** ここを無視して進むと、
// 誰の記録を出しているのか分からなくなる。
func (a *authenticator) userOf(token string) (string, bool) {
	if token == "" {
		return "", false
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	found, ok := a.sessions[token]
	if !ok {
		return "", false
	}
	if time.Now().After(found.expiresAt) {
		delete(a.sessions, token)
		return "", false
	}
	return found.userID, true
}

func (a *authenticator) revoke(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, token)
}

// allowAttempt はログイン試行を1回計上し、制限内かを返す。
func (a *authenticator) allowAttempt(remoteAddr string) bool {
	ip := ipOf(remoteAddr)

	a.mu.Lock()
	defer a.mu.Unlock()

	cutoff := time.Now().Add(-loginAttemptWindow)
	recent := make([]time.Time, 0, len(a.attempts[ip]))
	for _, at := range a.attempts[ip] {
		if at.After(cutoff) {
			recent = append(recent, at)
		}
	}
	if len(recent) >= loginAttemptLimit {
		a.attempts[ip] = recent
		return false
	}
	a.attempts[ip] = append(recent, time.Now())
	return true
}

func ipOf(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

type loginRequest struct {
	UserID string `json:"user_id"`

	// PasswordSha256 は平文パスワードの SHA-256 を小文字16進64桁にしたもの。
	//
	// gkill の画面と同じ形にしてある。**平文はブラウザから出さない。**
	PasswordSha256 string `json:"password_sha256"`
}

type loginResponse struct {
	OK     bool   `json:"ok"`
	UserID string `json:"user_id,omitempty"`
}

// handleLogin はログインを受け付ける。
//
// 判定の順序と理由は gkill の handle_login.go に合わせてある。
// **どこで弾かれたかは利用者に返さない。** 返すとアカウントの存在や
// 状態を外から探れてしまう。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 資格情報を見る前に計上する。gkill と同じ順序。
	if !s.auth.allowAttempt(r.RemoteAddr) {
		s.writeStatusError(w, http.StatusTooManyRequests,
			"ログインの試行が多すぎます。15分ほど待ってからやり直してください")
		return
	}

	request := loginRequest{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		s.writeStatusError(w, http.StatusBadRequest, "リクエストを解釈できません")
		return
	}

	userID := strings.TrimSpace(request.UserID)
	credential := strings.TrimSpace(request.PasswordSha256)

	if !credentialPattern.MatchString(credential) {
		// 画面が壊れているか、古い画面が平文を送っている。
		// 利用者への応答は他と同じにしたまま、記録には残す。
		s.logger.Warn("資格情報の書式が想定と違います",
			slog.String("from", ipOf(r.RemoteAddr)))
	}

	matched, reason, err := s.auth.verifier.Verify(ctx, userID, credential)
	if err != nil {
		s.logger.Warn("アカウントを照合できませんでした", slog.String("error", err.Error()))
		s.writeStatusError(w, http.StatusInternalServerError,
			"アカウントを確認できませんでした")
		return
	}
	if !matched {
		// 理由はログにだけ残す。応答は常に同じ文面。
		s.logger.Warn("確認画面へのログインに失敗しました",
			slog.String("from", ipOf(r.RemoteAddr)),
			slog.String("reason", string(reason)))
		s.writeStatusError(w, http.StatusUnauthorized, "利用者IDかパスワードが違います")
		return
	}

	token, err := s.auth.issue(userID)
	if err != nil {
		s.writeError(w, err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		// TLS で待ち受けているときだけ Secure を付ける。
		// gkill が TLS を切っている構成で付けると、クッキーが保存されず
		// ログインし続けられなくなる。
		Secure:   s.serveTLS,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	s.writeJSON(w, loginResponse{OK: true, UserID: userID})
}

// handleLogout はログイン状態を捨てる。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		s.auth.revoke(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.serveTLS,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	s.writeJSON(w, loginResponse{OK: true})
}

// sessionResponse は画面の出し分けに使う情報。
type sessionResponse struct {
	Authenticated bool   `json:"authenticated"`
	UserID        string `json:"user_id,omitempty"`

	// Analyzable はこの利用者の解析が起動時に用意されているか。
	//
	// 偽なら、ログインはできても提案は無い。起動時に --user へ渡されなかった
	// アカウントがこれに当たる。画面はその旨を出す。
	Analyzable bool `json:"analyzable"`
}

// handleSession はログイン済みかを返す。画面の出し分けに使う。
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.userOf(r)
	if !ok {
		s.writeJSON(w, sessionResponse{Authenticated: false})
		return
	}
	s.writeJSON(w, sessionResponse{
		Authenticated: true,
		UserID:        userID,
		Analyzable:    s.appFor(userID) != nil,
	})
}

// userOf は要求を出した利用者を返す。ログインしていなければ偽。
func (s *Server) userOf(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", false
	}
	return s.auth.userOf(cookie.Value)
}

// authedHandler はログイン済みの利用者IDを受け取るハンドラ。
type authedHandler func(w http.ResponseWriter, r *http.Request, userID string)

// requireAuth はログインしていない要求を弾き、利用者IDを渡す。
//
// **記録に触れる口はすべてこれを通す。** 1つでも漏れると、
// そこから他人の記録が読める。
func (s *Server) requireAuth(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, ok := s.userOf(r)
		if !ok {
			s.writeStatusError(w, http.StatusUnauthorized, "ログインしてください")
			return
		}
		next(w, r, userID)
	}
}
