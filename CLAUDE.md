# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

gkill_autocomplete は [gkill](https://github.com/mt3hr/gkill) の**衛星リポジトリ**。稼働中の gkill サーバを HTTP API 越しに読み、まだタグが付いていない記録に対して付けるべきタグを**提案する**。提案するだけで自分では書き込まず、確認画面で人が承認したときに初めて gkill の `/api/add_tag` を叩く。

**gkill 本体は1行も変更しない。** バイナリは `gkill_autocomplete` の一本で、判定エンジンと確認画面の Web サーバを兼ねる。

**gkill と同じ端末で動かす前提**。認証はそれを根拠にしている（下記）。

## 絶対に守ること（個人情報）

このアプリは生活ログの本文・写真・タグという最も機微なデータを直接扱う。以下は仕様であって、最適化や簡略化の対象ではない。

1. **LLM はループバック限定**。`llm.endpoint` が 127.0.0.1 / ::1 / localhost 以外なら、`allow_remote: true` が明示されていない限り**起動を拒否する**。クラウド LLM の API キー項目は設定スキーマに**用意しない**（書けなければ事故が起きない）
2. **通信先は gkill とローカル LLM の2つだけ**。テレメトリ・クラッシュレポート・更新チェックを入れない
3. **リポジトリに個人データを入れない**。資料の例示・テストのフィクスチャ・設定の雛形は**すべて架空の値**。実タグ名・実 rep 名・本文・画像をリポジトリ内のどのファイルにも書かない
4. **ログに本文を出さない**。既定は ID・件数・所要時間まで。本文・ファイル名・LLM のプロンプトは出さない
5. **設定に資格情報の項目を作らない**。認証は gkill の設定ディレクトリを直接見るので、パスワードもハッシュもセッションも持つ必要がない。`config_test.go` が「JSON に password / credential / session を含むキーが無い」ことを機械検査する
6. **記録に触れる口はすべて認証の後ろ**。一覧・承認・解析・写真の中継。1つでも漏れるとそこから全部読める
7. **Service Worker に記録を溜めさせない**。`/api` と `/thumb` は NetworkOnly。溜めてよいのは画面の器（JS/CSS/フォント/アイコン）だけ。溜めるとログアウトしても端末に残る
8. **利用者ごとに完全に分ける**。保存先の全表が `USER_ID` を持ち、すべての読み書きで絞る。写真の索引も利用者ごと
9. **LLM に渡すのは判定に必要な最小限だけ**。候補タグ名・対象の本文（または画像）・少数の参考例。ユーザID・rep 名・ファイルパス・位置情報は渡さない

## 認証（`internal/gkillauth`）

**gkill 本体のコードを import する。** `github.com/mt3hr/gkill/src/server` を GitHub から取る（`replace` は使わない。module 名と tag が食い違うので疑似バージョンで固定される）。依存は軽く、cobra や go-git は付いてこない。

**パスワードの照合を自前で書き直さない。** Argon2id のパラメータや比較の仕方がずれると、静かに弱くなる種類の間違いになる。`account.Account.VerifyPassword` をそのまま使う。

| 決まり | 理由 |
|---|---|
| CLI は `--user <利用者ID>` だけ。資格情報を受け取らない | セッションは `account_state.db` へ直接書いて発行する。gkill の `auto_tag` / `update_cache` と同じ手口で、信頼の根拠は「同じ端末で設定ディレクトリに書けること」 |
| `LoginSession.ApplicationName` は **`"gkill"` 固定** | gkill の認証が一致を要求する。違うと全 API が `AccountNotFoundError` で弾かれる |
| `ID` と `SessionID` に**別々の UUID** | `ID` は行の主キー、`SessionID` が鍵 |
| セッションは30分。期限が近ければ発行し直す | 確認画面は何時間も上がったまま。gkill のサブコマンドの5分では足りない |
| 終了時に発行したセッションを消す | gkill の DB に残り続けさせない |
| **`account.db` を DAO で開く前に必ずスキーマ版を確かめる** | 旧版(1.0.0)を開いた瞬間に gkill 側が自動移行を走らせ、**全アカウントのパスワードを無効化してリセットトークンを再発行する**。移行は gkill 自身にやらせる。検査は `sql.Open` + SELECT で行い、DAO を通さない（DAO のコンストラクタが移行の入口） |
| Windows の `$HOME` は自分で埋める | gkill では `main/common` の `init()` がやっているが、あれを import すると cobra が全部付いてくる。やらないと `os.ExpandEnv("$HOME/gkill")` が `/gkill` になる |
| 確認画面のログインは gkill の `/api/login` を**叩かない** | 叩くと gkill 側の回数（15分に10回）を消費し、総当たりを受けたとき**利用者自身が gkill に入れなくなる** |
| 弾く順序は gkill の `handle_login.go` に合わせる | 回数制限 → 有無 → 有効か → リセット中か → パスワード。リセットトークンが非nilなら**期限に関わらず**入れない |
| どこで弾いたかは応答に出さない | アカウントの存在や状態を探れないように。理由はログにだけ残す |
| 画面はブラウザで SHA-256 にしてから送る | gkill の画面と同じ形。平文をブラウザから出さない |

**TLS も gkill と同じものを使う。** `server_config.db` の `TLSCertFile` / `TLSKeyFile` を `os.ExpandEnv` して `ListenAndServeTLS` に渡す（DB には `$HOME/...` の未展開文字列で入っている）。gkill 側が TLS を切っていればこちらも平文で開く。

**証明書がどの名前を保証しているかは環境依存。** 載っていない名前で開くとブラウザが止まり、Service Worker も動かない（＝PWA を入れられない）。ここは実物を確かめてから案内すること。

## Build & Development Commands

| Command | Purpose |
|---|---|
| `npm run install_app` | フル build（フロント → embed → `go install`） |
| `npm run go_install` | Go のみ（フロント再ビルドなし） |
| `npm run go_mod` | go.mod / go.sum を作り直す |
| `npm run vet` | `go vet ./...` |
| `npm run build` | フロントのビルド（型検査 + vite build） |
| `npm run lint` | ESLint（自動修正あり） |
| `npm run verify_docs` | 資料の件数・リンク検査。`--list` で実測値を出す |
| `npm test` | `verify_docs` → Go テスト → フロントのユニットテスト |
| `npm run release` | クロスコンパイル |

**Go module root**: `src/autocomplete`（`go.mod` はここ）。module path は `github.com/mt3hr/gkill_autocomplete/src/autocomplete`。CGO 不要（pure Go SQLite）。

**embed**: `src/autocomplete/internal/embed/` はビルド成果物の置き場。中身は gitignore しつつ **`PLACEHOLDER.md` だけ追跡する**。空にすると `//go:embed` がコンパイルエラーになる。

## Architecture

```
cmd/gkill_autocomplete/   main（唯一のバイナリ）
internal/
  config/       設定の読み込みと検証。★LLM のループバック強制はここに閉じる
  gkillauth/    ★gkill のコードを import する唯一の場所（照合・セッション・TLS）
  gkillclient/  稼働中 gkill の HTTP クライアント
  llm/          OpenAI 互換クライアント（テキスト・画像共通）
  suggest/      学習と判定のエンジン
  store/        SQLite（提案 + 人間の判定。全表が USER_ID を持つ）
  websrv/       確認画面の配信と自前 API
  embed/        //go:embed
src/client/     Vue 3 + Vuetify 4（gkill のテーマを継承）+ PWA
```

`gkillclient` は「どうやってセッションを手に入れるか」を知らない。`SessionSource` インターフェース越しに受け取るので、認証の仕組みから独立している。

**`App` は1人ぶん。** 複数の利用者を扱うときは人数ぶん作り、`Store` だけを共有して行を `USER_ID` で分ける。

### gkill API を叩くときの確定事項

gkill 同梱の MCP 実装が雛形になるが、**そのまま写すと踏む穴が2つある**。

- **記録の取得は `/api/get_kyous` ではなく `/api/get_kyous_mcp`**。`/api/get_kyous` が返す `Kyou` は15フィールドだけで**本文もタイトルもタグも入らず、ページングも無い**。`get_kyous_mcp` は `tags` / `texts` / `payload` を1リクエストで返し `limit` / `cursor` / `max_size_mb` が使える
- **セッション取り直しの判定に `ERR000373`（期限切れ）を必ず含める**。MCP 実装は `ERR000002` / `ERR000013` / `ERR000238` しか見ておらず、期限を跨いだ瞬間に取り直さず落ちる
- **`add_tag` は ID とタイムスタンプを全部クライアントが生成する**。`related_time` を省くとゼロ値 `0001-01-01` になるので、**対象記録の `related_time` を必ず入れる**（MCP 実装はここを漏らしている）
- **`/api/login` は叩かない**。セッションはローカルの DB へ直接書いて発行するので、gkill 側のログイン回数（IP毎・15分に10回）を一切消費しない
- **業務エラーは HTTP 200 + `errors` 配列**で来る。成功時 `errors` は `[]` ではなく **`null`**。HTTP ステータスだけ見てはいけない
- **403 が返ったらまず gkill 側の `IsLocalOnlyAccess` を疑う**
- **`/files/` の認証は Cookie のみ**。`Cookie: gkill_session_id=<id>` が必須。URL は自前で組まず payload の `file_url` に base URL を足す
- **サムネは `?thumb=WxH`、各辺 1〜1024**。範囲外・書式違反は**エラーにならず原本（全画素）が返る**

### 判定の設計

- **multi-label**。候補タグごとに独立の yes/no + 確信度を出し、argmax を取らない。結果は **0個にも複数個にもなる**。0個は正常な答えであって失敗ではない
- **3階層。上で決まれば下へ行かない。** (1) 本文の完全一致・近傍一致を既タグ履歴と照合（LLM不要） (2) 近傍レコード・時刻事前確率・語彙一致（LLM不要） (3) LLM
- **タグを固定実装しない**。候補タグ集合は履歴の実績から決まる。設定の `rules` で上書きできるが、コードに特定のタグ名を書かない
- **冪等性**は決定的 UUIDv5（`uuid.NewSHA1(ns, targetID+"\x00"+tagName)`）。何度解析しても重複せず、**却下済みが復活しない**。承認時に gkill が返す `ERR000056 AlreadyExistTagError` は**成功扱い**にする（手で消したタグを蘇らせないため）

### 保存するもの

SQLite 1ファイル（リポジトリ外）。性質の違う2つが同居する。

| 内容 | 性質 | 失ったら |
|---|---|---|
| 提案（pending） | 派生データ。再解析で戻る | 困らない |
| 人間の判定（approved / rejected） | **再生成不可能** | 却下したはずの提案が永久に出続ける |

派生だけ捨てたいときは pending 行を消せばよい。判定は消さない。

**主キーは `(USER_ID, ID)` の複合。** あるアカウントが別のアカウントのリポジトリを
まとめて抱えている構成は普通にあり、その場合**同じ記録IDが両方に現れる**。
`ID` だけを主キーにすると、片方の判定がもう片方を上書きする。

## GUI

Vue 3.5 + Vuetify 4.1 + Vite 8 + TypeScript 6（gkill と同一メジャー）。**gkill のテーマを継承する**：ダークは星が降り、ライトは雪が降る。

移植で必ず踏む穴：

1. **`.v-layout--full-height { background-color: #0000 }` が唯一の透過スイッチ**。これが無いとオーバーレイは一切見えない
2. ダーク時の黒地は Vuetify が注入する `:root { color-scheme: dark }` 頼み
3. オーバーレイの `<style>` は **scoped 不可**（動的生成 DOM に scope 属性が付かない）
4. **`onUnmounted` でタイマー解除が必須**。テーマ切替が `v-if` なので、解除しないとループが多重化して流星が増え続ける

**Web フォントは MDI アイコンのみ**。テキスト用の Web フォントは読み込まない（gkill もそうで、実効は Vuetify 既定の `Roboto, sans-serif`）。MDI は woff2 のみに削る Vite プラグインを入れる。

### PWA

`vite-plugin-pwa`。アイコンは `src/tools/build_icons.mjs` が依存なしで描き、生成物を `public/` に置いて追跡する（毎回のビルドで作り直さない）。

**`/api` と `/thumb` は `NetworkOnly` かつ `navigateFallbackDenylist`。** 前者は記録の中身を端末に残さないため、後者は API の失敗が「壊れた JSON」として現れて原因が追えなくなるのを防ぐため。

テンプレートで `defineProps` の結果を `props` という名前にすると、`v-slot:activator="{ props }"` と**名前が衝突する**。スロット側を `{ props: tooltip_props }` に改名すること。

## Language

コード（変数名・コメント・コミットメッセージ）と資料は日本語。

## Documentation

`documents/reverse/` に **22本**（README 含む）。gkill 本体の `documents/reverse/` と**同じ分類**にしてある（行き来しやすくするため）。gkill にあってこちらに無いのは `dvnf-rep-type-spec` / `plugin-system` / `mcp-setup-guide` の3本で、いずれも該当する仕組みを持たないため。

`npm run verify_docs` が件数・リンク・参照パス・Mermaid の種別・架空値を機械検査し、`npm test` に組み込まれている。**数値を書いた資料を増やしたら `buildCountAssertions` に1行足すこと。** 足し忘れると、その資料にだけ古い数値が残り続ける。

**資料に書いてよいのは件数・構造・型であって、記録の中身ではない。** 例示は `SampleRep` / `タグA` 等の架空値だけ。
