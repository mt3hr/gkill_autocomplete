// Package gkillauth は gkill 本体の設定ディレクトリを使って認証を行う。
//
// パスワードの照合も、gkill を叩くためのセッション発行も、TLS 証明書の場所も、
// すべて gkill 自身のコード(github.com/mt3hr/gkill/src/server)に委ねる。
// Argon2id のパラメータや比較の仕方を写し取ると、ずれたときに
// 「静かに弱くなる」種類の間違いになるため、二重に実装しない。
package gkillauth

import (
	"os"
	"path/filepath"
	"strings"
)

// FixHomeEnv は Windows で空になりがちな $HOME を埋める。
//
// gkill 本体では main/common の init() が同じことをしている。
// そのパッケージは cobra などを丸ごと引き連れてくるので import せず、
// 必要な1行だけをこちらで持つ。やらないと os.ExpandEnv("$HOME/gkill") が
// "/gkill" になり、設定ディレクトリを見つけられない。
func FixHomeEnv() {
	if os.Getenv("HOME") == "" {
		os.Setenv("HOME", os.Getenv("HOMEDRIVE")+os.Getenv("HOMEPATH"))
	}
}

// GkillHome は gkill のホームディレクトリを返す。
//
// 優先順は「明示指定 > $GKILL_HOME > $HOME/gkill」。
// $GKILL_HOME は gkill がプラグインを起動するときに渡す環境変数でもあるので、
// gkill から起動された場合はそれに従う。
func GkillHome(configured string) string {
	FixHomeEnv()

	if strings.TrimSpace(configured) != "" {
		return filepath.Clean(os.ExpandEnv(strings.TrimSpace(configured)))
	}
	if fromEnv := strings.TrimSpace(os.Getenv("GKILL_HOME")); fromEnv != "" {
		return filepath.Clean(os.ExpandEnv(fromEnv))
	}
	return filepath.Clean(os.ExpandEnv("$HOME/gkill"))
}

// ConfigDir は gkill の設定ディレクトリを返す。
func ConfigDir(gkillHome string) string {
	return filepath.Join(gkillHome, "configs")
}

// AccountDBPath はアカウントDBの場所を返す。
func AccountDBPath(configDir string) string {
	return filepath.Join(configDir, "account.db")
}

// AccountStateDBPath はログインセッションDBの場所を返す。
func AccountStateDBPath(configDir string) string {
	return filepath.Join(configDir, "account_state.db")
}

// ServerConfigDBPath はサーバ設定DBの場所を返す。
func ServerConfigDBPath(configDir string) string {
	return filepath.Join(configDir, "server_config.db")
}
