---
name: autocomplete-analyze-run
description: "gkill_autocomplete の解析の走らせ方（src/autocomplete/internal/app/ の app.go・init.go・benchmark.go）の約束。解析はリクエストの寿命から切り離し r.Context() に縛らないこと（タブを閉じる・再読込する・端末が眠るだけで解析が死ぬ）、POST /api/analyze は即返して GET /api/analyze/status で見ること、解析の寿命は Server.baseCtx であること、判定が続けて失敗するなら打ち切ること（記録ではなく環境の問題なので走り続けても候補を消費するだけ）、FailureReason は決め打ちの文字列に落とすこと、init はリポジトリの中のファイルに決して書かないこと、benchmark の母集団を扱う。internal/app/ を編集するとき必読。「同じ場所で毎回止まる」「タブを閉じたら解析が死んだ」の調査でも必読。"
---

# 解析の走らせ方

対象: `src/autocomplete/internal/app/**`（`app.go` / `init.go` / `benchmark.go`）

**このファイルは全文が、破ると静かに壊れる約束である。該当作業では飛ばさずに読むこと。**

## 解析はリクエストの寿命から切り離す

**解析はリクエストの寿命から切り離す。** 写真の判定は1件で数分かかり、数十件あれば1時間を超える。
`POST /api/analyze` は開始を頼むだけで即座に返し、進み具合は `GET /api/analyze/status` で見る。
`r.Context()` に縛ると、タブを閉じる・再読込する・端末が眠るだけで解析が死ぬ。
解析の寿命は `Server.baseCtx`（＝待ち受けの文脈）で、Ctrl+C のときだけ止まる。

## 続けて失敗するなら打ち切る

```go
// **続けて失敗するなら打ち切る。**
// 記録ではなく環境の問題なので、走り続けても候補を消費するだけで
// 何も判定できない。飛ばした記録は評価済みにしていないので、
// ここで止めても失われるものは無い。
```

`maxConsecutiveJudgeFailures` に達したら `FailureReason` に最頻の理由を入れて止める。
全件失敗は接続先か設定の問題なので `Error`、一部失敗は `Warn` で言い方を変える
（判定の失敗の扱いは [autocomplete-suggest](../autocomplete-suggest/SKILL.md) が正本）。

## リポジトリの中のファイルには決して書かない

```go
// これは利用者自身の端末に出すものなので、リポジトリ名やタグ名を含んでよい。
// 一方でリポジトリの中のファイルには決して書かない。
```

`InitReport.Summary()` は端末の画面に出すもの。同じ文字列をリポジトリ内のファイルへ
書き出す経路を作らないこと。

## benchmark の母集団

候補にならない記録を混ぜると数字が実態からずれる。母集団の作り方を変えるときは、
前後の数字が比較できなくなることを承知のうえで行う。

## 関連スキル

- [autocomplete-suggest](../autocomplete-suggest/SKILL.md) — 判定そのものと失敗の扱い
- [autocomplete-websrv](../autocomplete-websrv/SKILL.md) — `/api/analyze` と status の口
- [autocomplete-store](../autocomplete-store/SKILL.md) — 解析結果の保存先
