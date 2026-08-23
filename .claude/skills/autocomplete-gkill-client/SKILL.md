---
name: autocomplete-gkill-client
description: "gkill_autocomplete が稼働中の gkill を HTTP API 越しに読む側（src/autocomplete/internal/gkillclient/）の確定事項。記録の取得は get_kyous ではなく get_kyous_mcp を使うこと、セッション取り直しの判定に ERR000373 を必ず含めること、add_tag は related_time を必ず入れること、業務エラーは HTTP 200 + errors 配列で成功時は null であること、403 は IsLocalOnlyAccess を疑うこと、files の認証は Cookie のみであること、thumb が画像以外にもエラーを返さないので Content-Type を必ず確かめること、画像判定は先頭バイトの実体を見て長さで足切りしないことを扱う。internal/gkillclient/api.go・client.go・errors.go・files.go・types.go を編集するとき必読。「動画を写真として LLM に渡した」の調査でも必読。"
---

# gkill API を叩くときの確定事項

対象: `src/autocomplete/internal/gkillclient/**`

**このファイルは全文が、破ると静かに壊れる約束である。該当作業では飛ばさずに読むこと。**

gkill 同梱の MCP 実装が雛形になるが、**そのまま写すと踏む穴が2つある**。

- **記録の取得は `/api/get_kyous` ではなく `/api/get_kyous_mcp`**。`/api/get_kyous` が返す `Kyou` は15フィールドだけで**本文もタイトルもタグも入らず、ページングも無い**。`get_kyous_mcp` は `tags` / `texts` / `payload` を1リクエストで返し `limit` / `cursor` / `max_size_mb` が使える
- **セッション取り直しの判定に `ERR000373`（期限切れ）を必ず含める**。MCP 実装は `ERR000002` / `ERR000013` / `ERR000238` しか見ておらず、期限を跨いだ瞬間に取り直さず落ちる
- **`add_tag` は ID とタイムスタンプを全部クライアントが生成する**。`related_time` を省くとゼロ値 `0001-01-01` になるので、**対象記録の `related_time` を必ず入れる**（MCP 実装はここを漏らしている）
- **`/api/login` は叩かない**。セッションはローカルの DB へ直接書いて発行するので、gkill 側のログイン回数（IP毎・15分に10回）を一切消費しない
- **業務エラーは HTTP 200 + `errors` 配列**で来る。成功時 `errors` は `[]` ではなく **`null`**。HTTP ステータスだけ見てはいけない
- **403 が返ったらまず gkill 側の `IsLocalOnlyAccess` を疑う**
- **`/files/` の認証は Cookie のみ**。`Cookie: gkill_session_id=<id>` が必須。URL は自前で組まず payload の `file_url` に base URL を足す
- **サムネは `?thumb=WxH`、各辺 1〜1024**。範囲外・書式違反は**エラーにならず原本（全画素）が返る**
- **`?thumb=` は画像以外にもエラーを返さない**。gkill は拡張子でサムネイルの対象かを決め、対象でなければサムネイルを作らずに**原本をそのまま 200 で返す**。動画・書庫・書類のいずれでも起きる。**応答の `Content-Type` が `image/` で始まるかを必ず確かめる**（`gkillclient.ErrNotAnImage`）。確かめないと、動画1本を読み取り上限まで読み込んだ上で LLM へ写真として渡すことになる
- **idf は画像とは限らない**。`payload.is_image` が偽の idf は写真も本文も持たないので、画面には `file_name` を出す。出さないと日時と種別しか載らない空の札になる

## 画像判定は先頭バイトの実体を見る

```go
// isDecodableImage は視覚モデルが復号できる形式かを先頭バイトで判定する。
//
// 拡張子でも Content-Type でもなく実体を見る。gkill は拡張子で画像かを決めており、
// RAW も画像として image/* で返してくるため、型情報は当てにならない。
// 見るのは先頭の数バイトだけ。長さで足切りはしない
// (短いものを弾く判定にすると、短い正当なヘッダを持つ検査用の画像まで落ちる)。
```

## セッションの取り方から独立している

`gkillclient` は「どうやってセッションを手に入れるか」を知らない。`SessionSource` インターフェース越しに受け取るので、認証の仕組みから独立している。この分離を崩さないこと。

## 関連スキル

- [autocomplete-gkill-auth](../autocomplete-gkill-auth/SKILL.md) — セッションの発行と破棄
- [autocomplete-suggest](../autocomplete-suggest/SKILL.md) — 取ってきた画像を渡す先
- [autocomplete-websrv](../autocomplete-websrv/SKILL.md) — IDの一覧を分割して渡す側
