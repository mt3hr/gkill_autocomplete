# 開発環境の構築とビルド

## 1. 前提

| 必要なもの | 版 | 用途 |
| --- | --- | --- |
| Go | 1.26.4 以上 | 本体。CGO は不要 |
| Node.js | 20.19 以上 | 確認画面のビルド |
| ローカル LLM | — | 判定に使う。開発だけなら無くてもよい |
| 稼働中の gkill | — | 記録の取得先。**同じ端末で動いていること** |

CGO が要らないのは、SQLite に pure Go の実装を使っているためです。

gkill と同じ端末で動かすのは、認証がそれを根拠にしているためです。
gkill の設定ディレクトリへ書けることが、そのまま権限になります。

### gkill 本体への依存

認証まわりだけ、gkill のコードを import しています。

```
require github.com/mt3hr/gkill/src/server v0.0.0-<疑似バージョン>
```

`replace` は使いません。GitHub から疑似バージョンで取ります。
module 名（`.../src/server`）とリポジトリのタグ（`vX.Y.Z`）が食い違うので、
版を上げるときは**コミットを指定して `go get`** してください。

```
go get github.com/mt3hr/gkill/src/server@<コミットハッシュ>
```

引き込まれる依存は `golang.org/x/crypto` と `modernc.org/sqlite` 程度で、
cobra や go-git は付いてきません（あれは gkill の `main/common` 側にあります）。

## 2. 最初のビルド

```
git clone <このリポジトリ>
cd gkill_autocomplete
npm i
npm run install_app
```

`install_app` は次を順に行います。

```
clean_dist          dist/ を消す
clean_embed         埋め込み先を消す
build               確認画面を型検査してビルド
copy_dist_to_embed  dist/ を埋め込み先へコピー
go_install          Go のバイナリを入れる
```

## 3. npm スクリプト

| コマンド | 内容 |
| --- | --- |
| `npm run dev` | 確認画面の開発サーバ。`/api` と `/thumb` は本体へ中継する |
| `npm run build` | 確認画面のビルド（型検査と並行） |
| `npm run type-check` | 型検査だけ |
| `npm run lint` | 確認画面の静的検査（自動修正あり） |
| `npm run go_install` | Go だけ入れ直す（確認画面は作り直さない） |
| `npm run go_mod` | `go.mod` と `go.sum` を作り直す |
| `npm run vet` | `go vet ./...` |
| `npm run verify_docs` | 資料の件数とリンクを検査。`-- --list` で実測値を表示 |
| `npm test` | 資料の検査 → Go のテスト → 確認画面のテスト |
| `npm run release` | クロスコンパイル |

## 4. 開発中の進め方

### 確認画面だけを直す

```
gkill_autocomplete --user <利用者ID>   # 別の端末で本体を動かしておく
npm run dev                            # 確認画面はこちらで
```

開発サーバは平文の `http://localhost:5173` で開きます。**`crypto.subtle` は
安全なコンテキストでしか使えません**が、`localhost` は例外として扱われるので
ログインは通ります。LAN の IP で開発サーバを見るとログインできません。

Service Worker は開発中は無効にしてあります（挟まると変更が反映されない原因が
分かりにくくなるため）。PWA の動きを見たいときは `npm run install_app` してから
本体を開いてください。

開発サーバは `/api` と `/thumb` を本体（既定 `127.0.0.1:9797`）へ中継します。
本体を作り直さずに画面だけを試せます。

### 本体だけを直す

```
npm run go_install
```

確認画面のビルドを飛ばせます。ただし**埋め込み先が空だとコンパイルできない**ので、
一度は `npm run install_app` を通しておいてください。

## 5. 埋め込みについて

確認画面のビルド結果は `src/autocomplete/internal/embed/html/` へコピーされ、
`//go:embed` でバイナリに入ります。

**このディレクトリの `PLACEHOLDER.md` を消してはいけません。** 中身は追跡しませんが、
ディレクトリが空になると `//go:embed` がパターン不一致でコンパイルエラーになります。

確認画面を組み込んでいないバイナリでも、解析（`--once`）と設定生成（`init`）は動きます。
画面を開こうとしたときだけ、作り直すよう案内します。

## 6. ローカル LLM の用意

OpenAI 互換の `/v1/chat/completions` を持つものであれば実装は問いません。

- **本文の判定**にはテキストのモデル
- **写真の判定**には視覚のモデル

どちらも設定していない場合、LLM の段階は静かに飛ばされ、逐語一致と近傍による判定だけで動きます。

**接続先はループバックでなければ起動を拒否します。** これは仕様であって、
開発中に外部のモデルを使うための抜け道もありません。

## 7. クロスコンパイル

```
npm run release
```

Windows / Linux（amd64・arm64）向けのバイナリを `release/` に作ります。
CGO を使っていないので、追加の道具は要りません。

## 8. つまずきやすいところ

| 症状 | 原因と対処 |
| --- | --- |
| `//go:embed` がコンパイルエラー | 埋め込み先が空。`npm run install_app` を通す |
| 起動時に接続先を拒否される | 仕様。ループバック以外は通らない |
| gkill が HTTP 403 を返す | gkill 側のローカル限定アクセス。同じ端末から実行する |
| ログインが通らなくなった | **15分に10回**の制限。待つ以外にない。確認スクリプトを連打しない |
| Windows で並列のテストが謎の失敗をする | プロセスが多すぎる。`go test -p 2` にする |
