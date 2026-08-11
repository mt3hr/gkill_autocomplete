package gkillauth

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mt3hr/gkill/src/server/gkill/dao/account"
	"github.com/mt3hr/gkill/src/server/gkill/dao/account_state"
	"github.com/mt3hr/gkill/src/server/gkill/dao/sqlite3impl"
)

const (
	// sessionApplicationName は発行するセッションのアプリケーション名。
	//
	// **"gkill" 以外にしてはいけない。** gkill の認証は
	// ApplicationName の一致を要求しており、違うと
	// 「アカウントが見つからない」として全 API が弾かれる。
	sessionApplicationName = "gkill"

	// sessionTTL は発行するセッションの有効期間。
	//
	// gkill のサブコマンドは1回の実行ぶん(5分)しか持たせないが、
	// このツールは確認画面を何時間も上げたままにする。
	// 長い期限を1回発行するのではなく、短命を発行し直して回す。
	sessionTTL = 30 * time.Minute

	// sessionRenewBefore は期限の何分前に発行し直すか。
	//
	// 解析の1往復が終わる前に切れないだけの余裕を取る。
	sessionRenewBefore = 10 * time.Minute
)

// SessionProvider は gkill を叩くためのセッションを、
// ローカルのDBへ直接書いて発行する。
//
// gkill 本体の auto_tag / update_cache と同じ手口で、
// **信頼の根拠は「gkill と同じ端末で設定ディレクトリに書けること」**。
// パスワードは要らないので、コマンドラインに資格情報を渡さずに済む。
type SessionProvider struct {
	configDir string
	userID    string
	device    string
	logger    *slog.Logger

	mu        sync.Mutex
	sessionID string
	expiresAt time.Time
}

// NewSessionProvider は SessionProvider を作る。
//
// この時点ではまだセッションを発行しない。最初に SessionID が呼ばれたときに作る。
func NewSessionProvider(configDir string, userID string, device string, logger *slog.Logger) *SessionProvider {
	return &SessionProvider{
		configDir: configDir,
		userID:    userID,
		device:    device,
		logger:    logger,
	}
}

// UserID は対象の利用者IDを返す。
func (p *SessionProvider) UserID() string { return p.userID }

// SessionID は有効なセッションIDを返す。期限が近ければ発行し直す。
func (p *SessionProvider) SessionID(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.sessionID != "" && time.Now().Before(p.expiresAt.Add(-sessionRenewBefore)) {
		return p.sessionID, nil
	}

	newSessionID, expiresAt, err := p.issue(ctx)
	if err != nil {
		return "", err
	}

	// 新しいものを取れてから古いものを消す。逆にすると、
	// 発行に失敗したときに手元のセッションまで失う。
	if p.sessionID != "" {
		p.deleteSession(ctx, p.sessionID)
	}

	p.sessionID = newSessionID
	p.expiresAt = expiresAt
	return p.sessionID, nil
}

// Invalidate は手元のセッションを捨てる。
//
// gkill が「セッションが無効」と答えたときに呼ぶ。次の SessionID で作り直す。
// gkill 側の行も消しておく(無効になった行を残しても意味が無いため)。
func (p *SessionProvider) Invalidate(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.sessionID == "" {
		return
	}
	p.deleteSession(ctx, p.sessionID)
	p.sessionID = ""
	p.expiresAt = time.Time{}
}

// Close は発行したセッションを消す。DBに残り続けないようにする。
func (p *SessionProvider) Close(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.sessionID == "" {
		return
	}
	p.deleteSession(ctx, p.sessionID)
	p.sessionID = ""
}

// issue はセッションを1つ発行する。
//
// 手順は gkill の main/common/password_admin.go の issueLocalSession と同じ。
// あちらは非公開なので呼べないが、使っている部品はすべて公開されている。
func (p *SessionProvider) issue(ctx context.Context) (string, time.Time, error) {
	accountDAO, err := account.NewAccountDAOSQLite3Impl(ctx, AccountDBPath(p.configDir))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("error at open account dao: %w", err)
	}
	defer func() {
		if err := accountDAO.Close(ctx); err != nil {
			p.logger.Debug("アカウントDBを閉じられませんでした", slog.String("error", err.Error()))
		}
	}()

	targetAccount, err := accountDAO.GetAccount(ctx, p.userID)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("error at get account %s: %w", p.userID, err)
	}
	if targetAccount == nil {
		return "", time.Time{}, fmt.Errorf("error: gkill に %s というアカウントがありません", p.userID)
	}
	if !targetAccount.IsEnable {
		return "", time.Time{}, fmt.Errorf("error: gkill のアカウント %s は無効です", p.userID)
	}

	sessionDAO, err := account_state.NewLoginSessionDAOSQLite3Impl(ctx, AccountStateDBPath(p.configDir))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("error at open login session dao: %w", err)
	}
	defer func() {
		if err := sessionDAO.Close(ctx); err != nil {
			p.logger.Debug("セッションDBを閉じられませんでした", slog.String("error", err.Error()))
		}
	}()

	expiresAt := time.Now().Add(sessionTTL)
	loginSession := &account_state.LoginSession{
		// ID は行の主キー、SessionID が鍵。別々のものを入れる。
		ID:              sqlite3impl.GenerateNewID(),
		UserID:          targetAccount.UserID,
		Device:          p.device,
		ApplicationName: sessionApplicationName,
		SessionID:       sqlite3impl.GenerateNewID(),
		ClientIPAddress: "127.0.0.1",
		LoginTime:       time.Now(),
		ExpirationTime:  expiresAt,
		IsLocalAppUser:  true,
	}
	if _, err := sessionDAO.AddLoginSession(ctx, loginSession); err != nil {
		return "", time.Time{}, fmt.Errorf("error at add login session: %w", err)
	}
	return loginSession.SessionID, expiresAt, nil
}

// deleteSession は発行済みのセッションを消す。失敗しても続行する。
func (p *SessionProvider) deleteSession(ctx context.Context, sessionID string) {
	sessionDAO, err := account_state.NewLoginSessionDAOSQLite3Impl(ctx, AccountStateDBPath(p.configDir))
	if err != nil {
		p.logger.Debug("セッションDBを開けませんでした", slog.String("error", err.Error()))
		return
	}
	defer func() {
		if err := sessionDAO.Close(ctx); err != nil {
			p.logger.Debug("セッションDBを閉じられませんでした", slog.String("error", err.Error()))
		}
	}()

	if _, err := sessionDAO.DeleteLoginSession(ctx, sessionID); err != nil {
		p.logger.Debug("セッションを消せませんでした", slog.String("error", err.Error()))
	}
}
