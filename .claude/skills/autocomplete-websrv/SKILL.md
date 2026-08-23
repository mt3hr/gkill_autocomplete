---
name: autocomplete-websrv
description: "gkill_autocomplete の自前 API（src/autocomplete/internal/websrv/server.go）の約束。記録IDの一覧は必ず分割して渡すこと（gkill の Mi 検索は5射影 UNION でバインド変数が 5N+5 になり SQLite の上限を N=6553 で超え、エラーではなく空の結果が HTTP 200 + errors null で返る。2026-08-18 に実際に踏んだ）、maxRecordsPerResponse は 200、gkill から消えた記録は Skipped に数えること、失敗の理由を error というキーで返さないこと、記録に触れる口はすべて requireAuth の後ろに置くこと、保存先の問い合わせは必ず利用者IDで絞ることを扱う。internal/websrv/server.go とそのテストを編集するとき必読。「確認待ちは残っているのに一覧だけが空」の調査でも必読。"
---

# 自前 API の不変条件

対象: `src/autocomplete/internal/websrv/server.go` と websrv のテスト群

**このファイルは全文が、実際に起きた事故の再発防止である。該当作業では飛ばさずに読むこと。**

## ★IDの一覧は必ず分割して渡すこと★

```go
// fetchRecordsChunkSize は1回のリクエストで gkill へ渡す記録IDの数。
//
// ★IDの一覧は必ず分割して渡すこと。★
//
// gkill の Mi 検索は5射影の UNION で、5本それぞれに ID の一覧を丸ごと
// 展開する(dao/reps/mi_repository_sqlite3_impl.go)。バインド変数は 5N+5 に
// なり、SQLite の上限 32766 を **N=6553 で超える**(実測: 6552 は成功、
// 6553 で破綻)。しかも超えたときに返るのはエラーではなく **空の結果** で、
// gkill 側のハンドラが「err はあるが GkillError は無い」場合に
// レスポンスへ何も積まないまま return するため、HTTP 200 + errors:null に
// 見える。2026-08-18 に確認待ちが上限を超える件数まで溜まった状態で実際に踏み、
// 「確認待ちは残っているのに一覧だけが空」という形で表面化した。
fetchRecordsChunkSize = 500
```

## 応答に載せる件数の上限

```go
// 画面は1件ずつ捌くので、確認待ち全部の中身を毎回引く必要はない。
// 全部引くと応答が大きくなるうえ、gkill 側の検索が数十秒かかる。
// 捌いて空になったら画面が読み直す(use-tag-suggestion-page.ts)。
maxRecordsPerResponse = 200
```

## gkill から消えた記録は Skipped に数える

```go
// gkill 側から消えた記録。画面には出さないが、数だけは伝える。
// 黙って捨てると、取得そのものが壊れたときに
// 「確認待ちは片付いた」と見分けが付かなくなる。
```

## 失敗の理由を `"error"` というキーで返さない

```go
// **失敗の理由を "error" というキーで返してはいけない。** 画面側の共通処理が
// その名前を「要求そのものが失敗した」と解釈して例外にするため、
// 解析の失敗とリクエストの失敗が混ざる。
```

## 認証と利用者の分離

- **記録に触れる口はすべて認証の後ろ**。一覧・承認・解析・写真の中継。**1つでも漏れるとそこから全部読める**
- 保存先の問い合わせは**必ず利用者IDで絞る**。空の利用者IDは `store.ErrEmptyUserID` で弾く
  （空を許すと、その行はどの利用者からも見えない迷子になるか、逆に条件次第で他人に見えてしまう）

## 関連スキル

- [autocomplete-gkill-auth](../autocomplete-gkill-auth/SKILL.md) — `requireAuth` とセッション
- [autocomplete-gkill-client](../autocomplete-gkill-client/SKILL.md) — 分割して渡す相手の API
- [autocomplete-store](../autocomplete-store/SKILL.md) — 複合主キーと `USER_ID`
- [autocomplete-analyze-run](../autocomplete-analyze-run/SKILL.md) — `/api/analyze` の寿命
