package websrv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/mt3hr/gkill/src/server/gkill/dao/account"

	"github.com/mt3hr/gkill_autocomplete/src/autocomplete/internal/gkillauth"
)

const (
	testUserID   = "testuser"
	testPassword = "testpass"

	// otherUserID は「ログインはできるが解析対象ではない」利用者。
	otherUserID   = "otheruser"
	otherPassword = "otherpass"

	// disabledUserID は無効にされた利用者。gkill と同じく入れない。
	disabledUserID = "disableduser"

	// noPasswordUserID はパスワード未設定の利用者。常に不一致になる。
	noPasswordUserID = "nopassworduser"

	// resettingUserID はパスワードリセット中の利用者。
	// トークンが残っている間は、期限に関わらず入れない。
	resettingUserID = "resettinguser"
)

// credentialOf は画面が送る資格情報(平文の SHA-256 を小文字16進64桁)を作る。
//
// 実装は本番にはもう無い。ブラウザが crypto.subtle で作るものを、
// テストから再現するためだけのもの。
func credentialOf(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

// newTestAccountDB は gkill と同じ形のアカウントDBを作る。
//
// gkill 自身の DAO をそのまま使うので、パスワードの保存も照合も
// **本番と同じ Argon2id の経路を通る**。ここを模造品にすると、
// 認証まわりの検査が意味を失う。
func newTestAccountDB(t *testing.T) string {
	t.Helper()

	ctx := context.Background()
	accountDBPath := filepath.Join(t.TempDir(), "account.db")

	accountDAO, err := account.NewAccountDAOSQLite3Impl(ctx, accountDBPath)
	if err != nil {
		t.Fatalf("アカウントDBを作れない: %v", err)
	}
	defer func() { _ = accountDAO.Close(ctx) }()

	add := func(target *account.Account) {
		t.Helper()
		if _, err := accountDAO.AddAccount(ctx, target); err != nil {
			t.Fatalf("アカウントを作れない (%s): %v", target.UserID, err)
		}
	}

	hashed := func(password string) *string {
		t.Helper()
		// 保存されるのは Argon2id(SHA-256(平文))。
		hash, err := account.HashPassword(credentialOf(password))
		if err != nil {
			t.Fatalf("パスワードを保存できない: %v", err)
		}
		return &hash
	}

	add(&account.Account{UserID: testUserID, PasswordHash: hashed(testPassword), IsEnable: true})
	add(&account.Account{UserID: otherUserID, PasswordHash: hashed(otherPassword), IsEnable: true})
	add(&account.Account{UserID: disabledUserID, PasswordHash: hashed(testPassword), IsEnable: false})
	// パスワード未設定。gkill では初回起動時の admin がこの形になる。
	add(&account.Account{UserID: noPasswordUserID, PasswordHash: nil, IsEnable: true})

	resetToken := "reset-token"
	expiration := time.Now().Add(account.PasswordResetTokenTTL)
	add(&account.Account{
		UserID:                       resettingUserID,
		PasswordHash:                 hashed(testPassword),
		IsEnable:                     true,
		PasswordResetToken:           &resetToken,
		PasswordResetTokenExpiration: &expiration,
	})

	return accountDBPath
}

// newTestVerifier はテスト用のアカウントDBに向いた照合器を返す。
func newTestVerifier(t *testing.T) *gkillauth.Verifier {
	t.Helper()
	return gkillauth.NewVerifier(newTestAccountDB(t), discardLogger())
}
