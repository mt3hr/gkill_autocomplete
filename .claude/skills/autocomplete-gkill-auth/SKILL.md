---
name: autocomplete-gkill-auth
description: "gkill_autocomplete の認証（src/autocomplete/internal/gkillauth/ と websrv/auth.go、cmd/）の約束。account.db を DAO で開く前に必ずスキーマ版を確かめること（旧版を開くと gkill が自動移行を走らせ全アカウントのパスワードを無効化する）、LoginSession.ApplicationName は gkill 固定、ID と SessionID は別々の UUID、セッションは1週間で終了時に必ず消すこと、確認画面のログインが gkill の /api/login を叩かないこと（叩くと利用者自身が gkill に入れなくなる）、弾く順序を handle_login.go に合わせること、どこで弾いたかを応答に出さないこと、TLS 設定の os.ExpandEnv、Windows の $HOME を扱う。internal/gkillauth/・websrv/auth.go・cmd/gkill_autocomplete/main.go を編集するとき必読。「gkill にログインできなくなった」の調査でも必読。"
---

# 認証の不変条件

対象: `src/autocomplete/internal/gkillauth/**` / `internal/websrv/auth.go` /
`cmd/gkill_autocomplete/main.go`

**このファイルは全文が、破ると静かに壊れる約束である。該当作業では飛ばさずに読むこと。**

**gkill 本体のコードを import する。** `github.com/mt3hr/gkill/src/server` を GitHub から取る（`replace` は使わない。module 名と tag が食い違うので疑似バージョンで固定される）。依存は軽く、cobra や go-git は付いてこない。

**パスワードの照合を自前で書き直さない。** Argon2id のパラメータや比較の仕方がずれると、静かに弱くなる種類の間違いになる。`account.Account.VerifyPassword` をそのまま使う。

| 決まり | 理由 |
|---|---|
| CLI は `--user <利用者ID>` だけ。資格情報を受け取らない | セッションは `account_state.db` へ直接書いて発行する。gkill の `auto_tag` / `update_cache` と同じ手口で、信頼の根拠は「同じ端末で設定ディレクトリに書けること」 |
| `LoginSession.ApplicationName` は **`"gkill"` 固定** | gkill の認証が一致を要求する。違うと全 API が `AccountNotFoundError` で弾かれる |
| `ID` と `SessionID` に**別々の UUID** | `ID` は行の主キー、`SessionID` が鍵 |
| セッションは1週間。期限が近ければ発行し直す | 確認画面は何日も上がったまま。gkill のサブコマンドの5分では足りない。長寿命の鍵を gkill の DB に置くことになるので、終了時に必ず消す |
| 終了時に発行したセッションを消す | gkill の DB に残り続けさせない |
| **`account.db` を DAO で開く前に必ずスキーマ版を確かめる** | 旧版(1.0.0)を開いた瞬間に gkill 側が自動移行を走らせ、**全アカウントのパスワードを無効化してリセットトークンを再発行する**。移行は gkill 自身にやらせる。検査は `sql.Open` + SELECT で行い、DAO を通さない（DAO のコンストラクタが移行の入口） |
| Windows の `$HOME` は自分で埋める | gkill では `main/common` の `init()` がやっているが、あれを import すると cobra が全部付いてくる。やらないと `os.ExpandEnv("$HOME/gkill")` が `/gkill` になる |
| 確認画面のログインは gkill の `/api/login` を**叩かない** | 叩くと gkill 側の回数（15分に10回）を消費し、総当たりを受けたとき**利用者自身が gkill に入れなくなる** |
| 弾く順序は gkill の `handle_login.go` に合わせる | 回数制限 → 有無 → 有効か → リセット中か → パスワード。リセットトークンが非nilなら**期限に関わらず**入れない |
| どこで弾いたかは応答に出さない | アカウントの存在や状態を探れないように。理由はログにだけ残す |
| 画面はブラウザで SHA-256 にしてから送る | gkill の画面と同じ形。平文をブラウザから出さない |

**TLS も gkill と同じものを使う。** `server_config.db` の `TLSCertFile` / `TLSKeyFile` を `os.ExpandEnv` して `ListenAndServeTLS` に渡す（DB には `$HOME/...` の未展開文字列で入っている）。gkill 側が TLS を切っていればこちらも平文で開く。

**証明書がどの名前を保証しているかは環境依存。** 載っていない名前で開くとブラウザが止まり、Service Worker も動かない（＝PWA を入れられない）。ここは実物を確かめてから案内すること。

## 記録に触れる口はすべて認証の後ろ

一覧・承認・解析・写真の中継。**1つでも漏れるとそこから全部読める。**
`requireAuth` の後ろに置くのを外さないこと。

## 関連スキル

- [autocomplete-websrv](../autocomplete-websrv/SKILL.md) — 認証の後ろに並ぶ自前 API
- [autocomplete-gkill-client](../autocomplete-gkill-client/SKILL.md) — セッションを使う側（`SessionSource`）
- [autocomplete-config-build-docs](../autocomplete-config-build-docs/SKILL.md) — 設定に資格情報の項目を作らない
