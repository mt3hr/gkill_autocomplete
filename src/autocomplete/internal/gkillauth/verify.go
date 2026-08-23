package gkillauth

// 編集前に読む: .claude/skills/autocomplete-gkill-auth/SKILL.md（この領域の不変条件の正本）

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/mt3hr/gkill/src/server/gkill/dao/account"
)

// DenyReason はログインを断った理由。
//
// **利用者には返さない。** どれに当たったかを教えると、
// アカウントの存在や状態を外から探れてしまう。記録に残すためだけに使う。
type DenyReason string

const (
	DenyNone              DenyReason = ""
	DenyAccountNotFound   DenyReason = "account_not_found"
	DenyAccountDisabled   DenyReason = "account_disabled"
	DenyPasswordResetting DenyReason = "password_resetting"
	DenyWrongPassword     DenyReason = "wrong_password"
)

// Verifier は gkill のアカウントで資格情報を照合する。
//
// 照合は手元の account.db に対して行い、gkill の /api/login は叩かない。
// 叩くと gkill 側のログイン回数(IP毎・15分に10回)を消費してしまい、
// この画面が総当たりを受けたときに
// **利用者自身が gkill へログインできなくなる**ため。
type Verifier struct {
	accountDBPath string
	logger        *slog.Logger
}

// NewVerifier は Verifier を作る。
//
// スキーマの検査は済んでいる前提。呼ぶ前に EnsureAccountSchemaIsCurrent を通すこと。
func NewVerifier(accountDBPath string, logger *slog.Logger) *Verifier {
	return &Verifier{accountDBPath: accountDBPath, logger: logger}
}

// Verify は資格情報がそのアカウントのものかを返す。
//
// credential は **平文パスワードの SHA-256 を小文字16進64桁にしたもの**。
// gkill の画面が送るのと同じ形で、保存値は Argon2id(SHA-256(平文)) になっている。
//
// 判定の順序は gkill の handle_login.go に合わせてある。
// とくにリセットトークンが残っているアカウントは、**期限に関わらず**入れない。
func (v *Verifier) Verify(ctx context.Context, userID string, credential string) (bool, DenyReason, error) {
	accountDAO, err := account.NewAccountDAOSQLite3Impl(ctx, v.accountDBPath)
	if err != nil {
		return false, DenyNone, fmt.Errorf("error at open account dao: %w", err)
	}
	defer func() {
		if err := accountDAO.Close(ctx); err != nil {
			v.logger.Debug("アカウントDBを閉じられませんでした", slog.String("error", err.Error()))
		}
	}()

	targetAccount, err := accountDAO.GetAccount(ctx, userID)
	if err != nil {
		return false, DenyNone, fmt.Errorf("error at get account: %w", err)
	}
	// 居ないアカウントは (nil, nil) で返る。
	if targetAccount == nil {
		return false, DenyAccountNotFound, nil
	}
	if !targetAccount.IsEnable {
		return false, DenyAccountDisabled, nil
	}
	if targetAccount.PasswordResetToken != nil {
		return false, DenyPasswordResetting, nil
	}

	// パスワード未設定のアカウントは常に不一致になる(fail-closed)。
	matched, err := targetAccount.VerifyPassword(credential)
	if err != nil {
		return false, DenyNone, fmt.Errorf("error at verify password: %w", err)
	}
	if !matched {
		return false, DenyWrongPassword, nil
	}
	return true, DenyNone, nil
}

// EnabledUserIDs は有効なアカウントの利用者IDを返す。起動時の確認に使う。
func (v *Verifier) EnabledUserIDs(ctx context.Context) ([]string, error) {
	accountDAO, err := account.NewAccountDAOSQLite3Impl(ctx, v.accountDBPath)
	if err != nil {
		return nil, fmt.Errorf("error at open account dao: %w", err)
	}
	defer func() {
		if err := accountDAO.Close(ctx); err != nil {
			v.logger.Debug("アカウントDBを閉じられませんでした", slog.String("error", err.Error()))
		}
	}()

	accounts, err := accountDAO.GetAllAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("error at get all accounts: %w", err)
	}

	userIDs := make([]string, 0, len(accounts))
	for _, a := range accounts {
		if a.IsEnable {
			userIDs = append(userIDs, a.UserID)
		}
	}
	return userIDs, nil
}
