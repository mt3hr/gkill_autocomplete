---
name: autocomplete-suggest
description: "gkill_autocomplete の判定エンジン（src/autocomplete/internal/suggest/・classify/・llm/・ids/）の設計。multi-label で argmax を取らず 0個も正常な答えであること、3階層で上で決まれば下へ行かないこと、タグ名をコードに書かないこと、冪等性は決定的 UUIDv5 で ERR000056 は成功扱いにすること、TierHopeless（keepRate が閾値未満の文脈では満点でも必ず落ちるので LLM を呼ばない・提案の中身は1件も変わらない）、maxAnswerTokens が無いと llama.cpp が生成を止めず壊れた JSON を返すこと、response_format を指定すること、近傍は前後1時間であること、LLM に渡すのは最小限だけであることを扱う。internal/suggest/engine.go・knowledge.go・record.go・classify/・llm/ を編集するとき必読。「解析が終わらない」「ErrBadResponse が出続ける」の調査でも必読。"
---

# 判定の設計

対象: `src/autocomplete/internal/suggest/**` / `internal/classify/**` / `internal/llm/**` / `internal/ids/**`

**このファイルは全文が、破ると静かに壊れる約束である。該当作業では飛ばさずに読むこと。**

- **multi-label**。候補タグごとに独立の yes/no + 確信度を出し、argmax を取らない。結果は **0個にも複数個にもなる**。0個は正常な答えであって失敗ではない
- **3階層。上で決まれば下へ行かない。** (1) 本文の完全一致・近傍一致を既タグ履歴と照合（LLM不要） (2) 近傍レコード・時刻事前確率・語彙一致（LLM不要） (3) LLM
- **タグを固定実装しない**。候補タグ集合は履歴の実績から決まる。設定の `rules` で上書きできるが、コードに特定のタグ名を書かない
- **冪等性**は決定的 UUIDv5（`uuid.NewSHA1(ns, targetID+"\x00"+tagName)`）。何度解析しても重複せず、**却下済みが復活しない**。承認時に gkill が返す `ERR000056 AlreadyExistTagError` は**成功扱い**にする（手で消したタグを蘇らせないため）
- **1件の判定の失敗で解析全体を落とさない**。LLM の時間切れ・応答の解釈失敗・写真が取れない、はどれも実際に起きる。落とすと後ろに並んだ何十件もが処理されず、しかも失敗した記録は評価済みにならないので**何度やり直しても同じ場所で止まり続ける**。飛ばして次へ進み、件数を `FailedRecords` で報告する。中断（`ctx` のキャンセル）だけは中断として扱う
- **飛ばすなら理由を残す。** 件数だけでは何をすればよいか分からない（LLM を落としたまま走らせると全件が失敗するが、件数しか出ないと原因に辿り着けない）。**エラー本文はそのまま出せない**（LLM の応答や記録の中身が混ざる）ので、`llm.Err*` / `classify.ErrImageUnavailable` / `gkillclient.ErrNotAnImage` を `errors.Is` で見分け、**決め打ちの文字列**に落として `FailureReason` に入れる。全件失敗は接続先か設定の問題なので `Error` で、一部失敗は `Warn` で言い方を変える

## 結果が必ず捨てられるなら LLM を呼ばない（TierHopeless）

```
// **結果が必ず捨てられるなら LLM を呼ばない。**
//
// LLM の確信度は最大 1.0 で、そのあと dampenByHabit が
// keepRate = 1 - UntaggedRate(文脈) を掛け、finish が閾値未満を落とす。
// つまり割り引き後の上限は keepRate そのものなので、
// keepRate が閾値に届かない文脈では、満点の答えが返っても必ず落ちる。
//
// **提案の中身は1件も変わらない。** 捨てられると分かっている答えを
// 作る手間だけが消える。
//
// これが効くのは、ほとんどタグを付けていない場所の記録が大半を占めるため。
// ある実環境では判定の61.7%が LLM に流れ、そのうち約9割が提案0個で
// 終わっていた。1件あたり5.3秒かかるので、16万件の判定が10日規模になっていた。
```

## LLM の応答は長さと文法の両方で縛る

```go
// maxAnswerTokens は1回の応答に許す長さ。
//
// **これが無いと応答が終わらないことがある。** 判定は temperature 0 の
// 貪欲デコードなので、モデルが同じ判定を繰り返し続ける状態に入りうる。
// 上限を渡さないと llama.cpp 側は n_predict を実質無制限として扱い、
// 文脈長を使い切るまで生成し続けたうえで、閉じ括弧の無い JSON を返す
// (2026-08-12 の実測: 1件に8分08秒かけて約4,460トークンを生成し、
// ExtractJSONObject が対応する "}" を見つけられず ErrBadResponse になった)。
//
// 正しい応答は候補6個でも200トークン前後で終わるので、512 で足りる。
const maxAnswerTokens = 512
```

```go
// ResponseFormat は JSON だけを返させるための指定。
//
// **お願いするだけでは足りない。** 指示文でも JSON だけを求めているが、
// 従わずに前置きを書いたり、途中で切れた JSON を返したりすることがある。
// ここで指定すると文法で縛られるので、壊れた JSON が原理的に作れなくなる。
```

## 近傍は前後1時間

```go
// 前後1時間を含めて数える。記録の時刻は数分ずれるのが普通なので。
```

一様分布なら 3/24 = 0.125。その4倍(=0.5)で振り切る目盛りにしてある。

## LLM に渡すのは判定に必要な最小限だけ

候補タグ名・対象の本文（または画像）・少数の参考例。
**ユーザID・rep 名・ファイルパス・位置情報は渡さない。**
`Reason` にも記録の本文を入れない（保存されて画面に出るため）。

## 関連スキル

- [autocomplete-analyze-run](../autocomplete-analyze-run/SKILL.md) — 判定を回す側（打ち切りと FailureReason）
- [autocomplete-gkill-client](../autocomplete-gkill-client/SKILL.md) — 画像を取ってくる側
- [autocomplete-store](../autocomplete-store/SKILL.md) — 提案の保存先
