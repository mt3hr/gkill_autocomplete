package gkillauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/mt3hr/gkill/src/server/gkill/dao/account"
	_ "modernc.org/sqlite"
)

// ErrAccountDBNotFound はアカウントDBが見つからないことを表す。
var ErrAccountDBNotFound = errors.New("account db not found")

// ErrAccountSchemaOutdated はアカウントDBのスキーマが現行版でないことを表す。
var ErrAccountSchemaOutdated = errors.New("account db schema is outdated")

// EnsureAccountSchemaIsCurrent は account.db が現行スキーマであることを確かめる。
//
// **これは飾りの検査ではない。** gkill の NewAccountDAOSQLite3Impl は、
// 旧スキーマ(1.0.0)のDBを開いた瞬間に自動移行を走らせ、
// **全アカウントのパスワードを無効化してリセットトークンを再発行する**。
// このツールがうっかり先に開くと、利用者は gkill にログインできなくなる。
//
// そこで、書き込みを一切しない方法で版だけを先に読み、
// 現行版でなければ何もせずに止める。移行は gkill 自身にやらせる。
func EnsureAccountSchemaIsCurrent(ctx context.Context, accountDBPath string) error {
	// sql.Open はファイルが無ければ作ってしまうので、先に実在を確かめる。
	if _, err := os.Stat(accountDBPath); err != nil {
		return fmt.Errorf("%w: %s", ErrAccountDBNotFound, accountDBPath)
	}

	// DAO を通さずに開く。DAO のコンストラクタが移行の入口なので、
	// ここで使ってはいけない。
	db, err := sql.Open("sqlite", "file:"+accountDBPath+"?_pragma=busy_timeout(6000)")
	if err != nil {
		return fmt.Errorf("error at open account db: %w", err)
	}
	defer func() { _ = db.Close() }()

	version := ""
	err = db.QueryRowContext(ctx,
		`SELECT VALUE FROM GKILL_META_INFO WHERE KEY = ?`,
		"SCHEMA_VERSION_ACCOUNT",
	).Scan(&version)
	if err != nil {
		// 行が無い、あるいは GKILL_META_INFO 自体が無い古いDB。
		// どちらも「gkill に開かせて移行させるべき」状態なので同じ扱いにする。
		return fmt.Errorf("%w: %s のスキーマ版を読み取れません(%v)。"+
			"先に gkill を起動して移行を済ませてください",
			ErrAccountSchemaOutdated, accountDBPath, err)
	}

	if version != account.CURRENT_SCHEMA_VERSION_ACCOUNT_DAO {
		return fmt.Errorf("%w: %s のスキーマ版が %s です(このツールは %s を前提にします)。"+
			"先に gkill を起動して移行を済ませてください",
			ErrAccountSchemaOutdated, accountDBPath, version,
			account.CURRENT_SCHEMA_VERSION_ACCOUNT_DAO)
	}
	return nil
}
