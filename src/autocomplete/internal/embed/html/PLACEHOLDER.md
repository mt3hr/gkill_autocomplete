このディレクトリには `npm run build` が `dist/` の中身をコピーします。

このファイルは消さないこと。ディレクトリが空だと `//go:embed` が
「no matching files found」で失敗し、`go build` が通らなくなります。
