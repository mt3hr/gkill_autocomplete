package gkillauth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mt3hr/gkill/src/server/gkill/dao/account"
)

const (
	testUserID   = "testuser"
	testPassword = "testpass"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// credentialOf は画面が送る資格情報(平文の SHA-256 を小文字16進64桁)を作る。
func credentialOf(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

// newAccountDB は gkill と同じ形のアカウントDBを作る。
//
// gkill 自身の DAO を使うので、保存も照合も本番と同じ Argon2id の経路を通る。
func newAccountDB(t *testing.T, accounts ...*account.Account) string {
	t.Helper()

	ctx := context.Background()
	accountDBPath := filepath.Join(t.TempDir(), "account.db")

	accountDAO, err := account.NewAccountDAOSQLite3Impl(ctx, accountDBPath)
	if err != nil {
		t.Fatalf("アカウントDBを作れない: %v", err)
	}
	defer func() { _ = accountDAO.Close(ctx) }()

	for _, target := range accounts {
		if _, err := accountDAO.AddAccount(ctx, target); err != nil {
			t.Fatalf("アカウントを作れない (%s): %v", target.UserID, err)
		}
	}
	return accountDBPath
}

func hashedPassword(t *testing.T, password string) *string {
	t.Helper()
	hash, err := account.HashPassword(credentialOf(password))
	if err != nil {
		t.Fatalf("パスワードを保存できない: %v", err)
	}
	return &hash
}

func TestEnsureAccountSchemaIsCurrentAcceptsCurrentSchema(t *testing.T) {
	accountDBPath := newAccountDB(t)

	if err := EnsureAccountSchemaIsCurrent(context.Background(), accountDBPath); err != nil {
		t.Fatalf("現行スキーマなのに拒否された: %v", err)
	}
}

func TestEnsureAccountSchemaIsCurrentRejectsMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "account.db")

	err := EnsureAccountSchemaIsCurrent(context.Background(), missing)
	if !errors.Is(err, ErrAccountDBNotFound) {
		t.Fatalf("ErrAccountDBNotFound を期待したが %v", err)
	}
	// 検査で作ってしまっていないこと。空のDBを置くと、
	// 次に gkill が開いたときに初期化されたと誤認されうる。
	if _, statErr := os.Stat(missing); statErr == nil {
		t.Error("検査したファイルを作ってしまっている")
	}
}

// **このアプリで一番危ない操作を止める検査。**
//
// gkill の NewAccountDAOSQLite3Impl は旧スキーマのDBを開いた瞬間に
// 自動移行を走らせ、全アカウントのパスワードを無効化する。
// このツールがうっかり開くと、利用者は gkill にログインできなくなる。
func TestEnsureAccountSchemaIsCurrentRefusesOldSchema(t *testing.T) {
	accountDBPath := newAccountDB(t)

	// 版だけ古いものに書き換える(移行そのものは gkill にやらせる)。
	db, err := sql.Open("sqlite", "file:"+accountDBPath)
	if err != nil {
		t.Fatalf("DBを開けない: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE GKILL_META_INFO SET VALUE = ? WHERE KEY = ?`,
		"1.0.0", "SCHEMA_VERSION_ACCOUNT"); err != nil {
		t.Fatalf("版を書き換えられない: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("DBを閉じられない: %v", err)
	}

	err = EnsureAccountSchemaIsCurrent(context.Background(), accountDBPath)
	if !errors.Is(err, ErrAccountSchemaOutdated) {
		t.Fatalf("ErrAccountSchemaOutdated を期待したが %v", err)
	}
	// 次に何をすればよいかを言うこと。
	if !strings.Contains(err.Error(), "gkill を起動") {
		t.Errorf("直し方が書かれていない: %v", err)
	}
}

func TestVerifyAcceptsCorrectCredential(t *testing.T) {
	accountDBPath := newAccountDB(t, &account.Account{
		UserID:       testUserID,
		PasswordHash: hashedPassword(t, testPassword),
		IsEnable:     true,
	})

	verifier := NewVerifier(accountDBPath, discardLogger())
	matched, reason, err := verifier.Verify(context.Background(), testUserID, credentialOf(testPassword))
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if !matched {
		t.Fatalf("正しい資格情報が通らない (reason=%s)", reason)
	}
}

func TestVerifyRejects(t *testing.T) {
	resetToken := "reset-token"
	// 期限切れのトークン。gkill は**期限に関わらず**ログインを拒む。
	expiredAt := time.Now().Add(-24 * time.Hour)

	accountDBPath := newAccountDB(t,
		&account.Account{UserID: testUserID, PasswordHash: hashedPassword(t, testPassword), IsEnable: true},
		&account.Account{UserID: "disabled", PasswordHash: hashedPassword(t, testPassword), IsEnable: false},
		&account.Account{UserID: "nopassword", PasswordHash: nil, IsEnable: true},
		&account.Account{
			UserID:                       "resetting",
			PasswordHash:                 hashedPassword(t, testPassword),
			IsEnable:                     true,
			PasswordResetToken:           &resetToken,
			PasswordResetTokenExpiration: &expiredAt,
		},
	)
	verifier := NewVerifier(accountDBPath, discardLogger())

	cases := []struct {
		name       string
		userID     string
		credential string
		wantReason DenyReason
	}{
		{name: "パスワードが違う", userID: testUserID, credential: credentialOf("wrong"), wantReason: DenyWrongPassword},
		{name: "居ないアカウント", userID: "nobody", credential: credentialOf(testPassword), wantReason: DenyAccountNotFound},
		{name: "無効なアカウント", userID: "disabled", credential: credentialOf(testPassword), wantReason: DenyAccountDisabled},
		// パスワード未設定は常に不一致(fail-closed)。
		{name: "パスワード未設定", userID: "nopassword", credential: credentialOf(""), wantReason: DenyWrongPassword},
		// トークンが残っている限り、期限切れでも入れない。
		{name: "リセット中", userID: "resetting", credential: credentialOf(testPassword), wantReason: DenyPasswordResetting},
		{name: "資格情報が空", userID: testUserID, credential: "", wantReason: DenyWrongPassword},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			matched, reason, err := verifier.Verify(context.Background(), testCase.userID, testCase.credential)
			if err != nil {
				t.Fatalf("想定外のエラー: %v", err)
			}
			if matched {
				t.Fatal("通ってしまった")
			}
			if reason != testCase.wantReason {
				t.Errorf("reason = %q, want %q", reason, testCase.wantReason)
			}
		})
	}
}

func TestVerifyRejectsPlainPasswordSentAsCredential(t *testing.T) {
	// 画面は平文ではなく SHA-256 を送る。平文を送っても通らないこと
	// (古い画面が繋がっても、通ってしまうより弾かれるほうがよい)。
	accountDBPath := newAccountDB(t, &account.Account{
		UserID:       testUserID,
		PasswordHash: hashedPassword(t, testPassword),
		IsEnable:     true,
	})

	verifier := NewVerifier(accountDBPath, discardLogger())
	matched, _, err := verifier.Verify(context.Background(), testUserID, testPassword)
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if matched {
		t.Error("平文のパスワードで通ってしまった")
	}
}

func TestEnabledUserIDs(t *testing.T) {
	accountDBPath := newAccountDB(t,
		&account.Account{UserID: "enabled-a", PasswordHash: hashedPassword(t, testPassword), IsEnable: true},
		&account.Account{UserID: "enabled-b", PasswordHash: hashedPassword(t, testPassword), IsEnable: true},
		&account.Account{UserID: "disabled", PasswordHash: hashedPassword(t, testPassword), IsEnable: false},
	)

	verifier := NewVerifier(accountDBPath, discardLogger())
	userIDs, err := verifier.EnabledUserIDs(context.Background())
	if err != nil {
		t.Fatalf("想定外のエラー: %v", err)
	}
	if len(userIDs) != 2 {
		t.Errorf("有効なアカウント = %v, want 2件", userIDs)
	}
	for _, userID := range userIDs {
		if userID == "disabled" {
			t.Error("無効なアカウントが混ざっている")
		}
	}
}

func TestGkillHomePrecedence(t *testing.T) {
	// 明示指定 > $GKILL_HOME > $HOME/gkill の順。
	// gkill から起動された場合は $GKILL_HOME が渡ってくるので、それに従う。
	t.Setenv("GKILL_HOME", filepath.Join("C:", "from-env"))

	explicit := filepath.Join("C:", "explicit")
	if got := GkillHome(explicit); got != filepath.Clean(explicit) {
		t.Errorf("明示指定が優先されない: %q", got)
	}

	if got := GkillHome(""); got != filepath.Clean(filepath.Join("C:", "from-env")) {
		t.Errorf("$GKILL_HOME が使われない: %q", got)
	}

	t.Setenv("GKILL_HOME", "")
	fallback := GkillHome("")
	if fallback == "" || strings.HasPrefix(fallback, string(filepath.Separator)+"gkill") {
		// Windows で $HOME が空だと "/gkill" になる。FixHomeEnv がそれを防ぐ。
		t.Errorf("$HOME が埋まっていない: %q", fallback)
	}
	if !strings.HasSuffix(fallback, "gkill") {
		t.Errorf("既定が $HOME/gkill になっていない: %q", fallback)
	}
}

func TestConfigDBPaths(t *testing.T) {
	configDir := ConfigDir(filepath.Join("C:", "home", "gkill"))

	cases := map[string]string{
		AccountDBPath(configDir):      "account.db",
		AccountStateDBPath(configDir): "account_state.db",
		ServerConfigDBPath(configDir): "server_config.db",
	}
	for path, want := range cases {
		if filepath.Base(path) != want {
			t.Errorf("%q の名前が違う, want %q", path, want)
		}
		if filepath.Base(filepath.Dir(path)) != "configs" {
			t.Errorf("%q が configs の下にない", path)
		}
	}
}
