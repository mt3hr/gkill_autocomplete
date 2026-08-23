---
name: autocomplete-store
description: "gkill_autocomplete の保存先（src/autocomplete/internal/store/）の約束。提案（pending）は派生データで再解析すれば戻るが人間の判定（approved / rejected）は再生成不可能で、消すと却下したはずの提案が永久に出続けること、主キーは (USER_ID, ID) の複合であること（あるアカウントが別のアカウントのリポジトリを抱えている構成では同じ記録IDが両方に現れ、ID だけを主キーにすると片方の判定がもう片方を上書きする）、全表が USER_ID を持ちすべての読み書きで絞ること、空の利用者IDを弾くこと、Reason に記録の本文を入れないことを扱う。internal/store/store.go・documents/reverse/er-diagram.md を編集するとき、表や列を足すとき必読。「却下した提案がまた出てくる」の調査でも必読。"
---

# 保存するものの不変条件

対象: `src/autocomplete/internal/store/**` /
[documents/reverse/er-diagram.md](../../../documents/reverse/er-diagram.md)

**このファイルは全文が、破ると静かに壊れる約束である。該当作業では飛ばさずに読むこと。**

## 性質の違う2つが同居する

SQLite 1ファイル（リポジトリ外）。

| 内容 | 性質 | 失ったら |
|---|---|---|
| 提案（pending） | 派生データ。再解析で戻る | 困らない |
| 人間の判定（approved / rejected） | **再生成不可能** | 却下したはずの提案が永久に出続ける |

派生だけ捨てたいときは pending 行を消せばよい。**判定は消さない。**

## 主キーは `(USER_ID, ID)` の複合

**主キーは `(USER_ID, ID)` の複合。** あるアカウントが別のアカウントのリポジトリを
まとめて抱えている構成は普通にあり、その場合**同じ記録IDが両方に現れる**。
`ID` だけを主キーにすると、片方の判定がもう片方を上書きする。

## 利用者ごとに完全に分ける

**利用者ごとに完全に分ける。** 保存先の全表が `USER_ID` を持ち、すべての読み書きで絞る。
写真の索引も利用者ごと。

```go
// ErrEmptyUserID は利用者IDが空のまま保存先を触ろうとしたことを表す。
//
// 空を許すと、その行はどの利用者からも見えない迷子になるか、
// 逆に条件次第で他人に見えてしまう。必ず弾く。
```

## Reason に記録の本文を入れない

保存されて画面に出るため。判定の根拠は種別と数値で表す。

## 関連スキル

- [autocomplete-websrv](../autocomplete-websrv/SKILL.md) — 利用者IDで絞る側
- [autocomplete-suggest](../autocomplete-suggest/SKILL.md) — 決定的 UUIDv5 と「却下済みが復活しない」
