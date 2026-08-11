// Package embed は確認画面のファイル一式をバイナリに埋め込む。
//
// 中身は npm run build が作り、prepare_install が html/ へコピーする。
// html/ が空になると go:embed がコンパイルエラーになるため、
// PLACEHOLDER.md だけは追跡対象として常に置いてある。
package embed

import (
	"embed"
	"io/fs"
)

//go:embed all:html
var htmlFS embed.FS

// Frontend は確認画面のファイル群を返す。
func Frontend() (fs.FS, error) {
	return fs.Sub(htmlFS, "html")
}

// IsBuilt は画面が組み込まれているかを返す。
//
// PLACEHOLDER.md しか無い状態、つまり npm run build を通していない
// バイナリでは偽になる。その場合は何をすればよいかを案内する。
func IsBuilt() bool {
	frontend, err := Frontend()
	if err != nil {
		return false
	}
	if _, err := fs.Stat(frontend, "index.html"); err != nil {
		return false
	}
	return true
}
