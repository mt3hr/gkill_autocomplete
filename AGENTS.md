# AGENTS.md

このリポジトリで作業する AI エージェント共通の入口（Codex CLI / Cursor / Claude Code / Gemini CLI / Copilot 等）。
Claude Code は `CLAUDE.md` の import 経由でこのファイルを読み込む。**規約の編集は必ずこのファイル側で行うこと。**

## Project Overview

gkill_autocomplete は [gkill](https://github.com/mt3hr/gkill) の**衛星リポジトリ**。稼働中の gkill サーバを HTTP API 越しに読み、まだタグが付いていない記録に対して付けるべきタグを**提案する**。提案するだけで自分では書き込まず、確認画面で人が承認したときに初めて gkill の `/api/add_tag` を叩く。

**gkill 本体は1行も変更しない。** バイナリは `gkill_autocomplete` の一本で、判定エンジンと確認画面の Web サーバを兼ねる。

**gkill と同じ端末で動かす前提**。認証はそれを根拠にしている。

## 絶対に守ること（個人情報）

このアプリは生活ログの本文・写真・タグという最も機微なデータを直接扱う。以下は仕様であって、最適化や簡略化の対象ではない。
**各条の理由と実装上の落とし穴は、下のルーティング表が指すスキルにある。**

1. **LLM はループバック限定** → [autocomplete-config-build-docs](.claude/skills/autocomplete-config-build-docs/SKILL.md)
2. **通信先は gkill とローカル LLM の2つだけ**。テレメトリ・クラッシュレポート・更新チェックを入れない
3. **リポジトリに個人データを入れない**。資料の例示・テストのフィクスチャ・設定の雛形は**すべて架空の値**。実タグ名・実 rep 名・本文・画像をリポジトリ内のどのファイルにも書かない
4. **ログに本文を出さない**。既定は ID・件数・所要時間まで
5. **設定に資格情報の項目を作らない** → [autocomplete-config-build-docs](.claude/skills/autocomplete-config-build-docs/SKILL.md)
6. **記録に触れる口はすべて認証の後ろ**。1つでも漏れるとそこから全部読める → [autocomplete-websrv](.claude/skills/autocomplete-websrv/SKILL.md)
7. **Service Worker に記録を溜めさせない** → [autocomplete-client-pwa](.claude/skills/autocomplete-client-pwa/SKILL.md)
8. **利用者ごとに完全に分ける** → [autocomplete-store](.claude/skills/autocomplete-store/SKILL.md)
9. **LLM に渡すのは判定に必要な最小限だけ** → [autocomplete-suggest](.claude/skills/autocomplete-suggest/SKILL.md)

## 触る場所ごとの必読資料（ルーティング表）

下の表で**触るファイルに一致する行があれば、編集を始める前に「先に読む」のスキルファイルを Read すること**。
gkill_autocomplete の不変条件の多くは「例外もエラーも出さずに静かに壊れる」種類で、読まずに書くと3列目が起きる。
パス連動のスキル機構を持たないエージェントも、これらはただの Markdown なので同じパスを Read すればよい。

<!-- ROUTING-TABLE:BEGIN 手書きの表。スキルの増減時は必ずここも更新する（verify_docs の checkSkills が双方向網羅を検査） -->

| 触るファイル | 先に読む | 読み落とすと |
|---|---|---|
| `src/autocomplete/internal/gkillauth/**`・`internal/websrv/auth.go`・`cmd/gkill_autocomplete/main.go` | [autocomplete-gkill-auth](.claude/skills/autocomplete-gkill-auth/SKILL.md) | 旧版の `account.db` を DAO で開いた瞬間に gkill 側が自動移行を走らせ、**全アカウントのパスワードを無効化してリセットトークンを再発行する**／`ApplicationName` が `"gkill"` でないと全 API が `AccountNotFoundError` で弾かれる／確認画面のログインで gkill の `/api/login` を叩くと回数制限（15分に10回）を消費し、総当たりを受けたとき**利用者自身が gkill に入れなくなる**／終了時にセッションを消し忘れると長寿命の鍵が gkill の DB に残り続ける |
| `src/autocomplete/internal/gkillclient/**` | [autocomplete-gkill-client](.claude/skills/autocomplete-gkill-client/SKILL.md) | `/api/get_kyous` を使うと本文もタイトルもタグも入らずページングも無い／`ERR000373` を取り直しの判定に含めないと期限を跨いだ瞬間に落ちる／`add_tag` で `related_time` を省くとゼロ値 `0001-01-01` になる／`?thumb=` は画像以外にもエラーを返さないので、`Content-Type` を確かめないと**動画1本を読み取り上限まで読み込んだ上で LLM へ写真として渡す**／HTTP ステータスだけ見ると HTTP 200 + `errors` 配列の業務エラーを取りこぼす |
| `src/autocomplete/internal/suggest/**`・`internal/classify/**`・`internal/llm/**`・`internal/ids/**` | [autocomplete-suggest](.claude/skills/autocomplete-suggest/SKILL.md) | `maxAnswerTokens` を外すと llama.cpp が `n_predict` を実質無制限扱いし、**1件に8分08秒かけて約4,460トークン生成したうえで閉じ括弧の無い JSON を返す**（2026-08-12 実測）／`response_format` を外すと指示文だけでは従わず壊れた JSON が来る／`TierHopeless` を外すと、実環境で判定の61.7%が LLM に流れそのうち約9割が提案0個で終わる（提案の中身は1件も変わらない）／argmax を取ると 0個という正常な答えが失敗に化ける |
| `src/autocomplete/internal/app/**` | [autocomplete-analyze-run](.claude/skills/autocomplete-analyze-run/SKILL.md) | 解析を `r.Context()` に縛ると、タブを閉じる・再読込する・端末が眠るだけで解析が死ぬ／連続失敗の打ち切りを外すと、環境の問題で全件失敗しているのに候補だけを消費し続ける／`FailureReason` にエラー本文をそのまま入れると LLM の応答や記録の中身が混ざる |
| `src/autocomplete/internal/websrv/server.go` | [autocomplete-websrv](.claude/skills/autocomplete-websrv/SKILL.md) | 記録IDの一覧を分割せずに渡すと、gkill の Mi 検索でバインド変数が 5N+5 になり SQLite の上限を **N=6553 で超え**、エラーではなく**空の結果が HTTP 200 + `errors:null` で返る**（2026-08-18 に「確認待ちは残っているのに一覧だけが空」として実際に踏んだ）／失敗の理由を `"error"` というキーで返すと画面側が「要求そのものの失敗」と解釈して例外にする／消えた記録を `Skipped` に数えないと「取得が壊れた」と「片付いた」が見分けられない |
| `src/autocomplete/internal/store/**` | [autocomplete-store](.claude/skills/autocomplete-store/SKILL.md) | 主キーを `ID` だけにすると、あるアカウントが別のアカウントのリポジトリを抱えている構成で**片方の判定がもう片方を上書きする**／人間の判定（approved / rejected）は再生成不可能で、消すと却下したはずの提案が永久に出続ける／`USER_ID` で絞り忘れた読み書きが1つでもあると他人の行が見える |
| `src/client/**`・`vite.config.ts`・`src/tools/build_icons.mjs`・`public/**` | [autocomplete-client-pwa](.claude/skills/autocomplete-client-pwa/SKILL.md) | `.v-layout--full-height { background-color: #0000 }` が無いとオーバーレイが一切見えない／オーバーレイの `<style>` を scoped にすると動的生成 DOM に scope 属性が付かず効かない／`onUnmounted` でタイマーを解除しないとループが多重化して流星が増え続ける／`/api` と `/thumb` を NetworkOnly から外すと、ログアウトしても記録が端末に残る |
| `src/autocomplete/internal/config/**`・`config.example.json`・`package.json`・`src/tools/**`・`documents/**`・`AGENTS.md`・`CLAUDE.md`・`.claude/skills/**`・`.gitignore`・`.gitattributes` | [autocomplete-config-build-docs](.claude/skills/autocomplete-config-build-docs/SKILL.md) | `llm.endpoint` のループバック強制を緩めると、生活ログの本文と写真がクラウドへ出る／設定に資格情報の項目を作ると `config_test.go` が落ちる／`ScopeConfig` を絞らないと他のツールが自動収集した記録まで拾う／保存時に `Redacted` を通すと伏せ字が書き込まれて動かなくなる／`embed/html/PLACEHOLDER.md` を消すと `//go:embed` がコンパイルエラーになる／`npm test` に含まれる `verify_docs` が落ちる |

<!-- ROUTING-TABLE:END -->

### 症状から引く

| 症状 | 読む |
|---|---|
| gkill にログインできなくなった／パスワードがリセットされた | autocomplete-gkill-auth |
| 全 API が AccountNotFoundError で弾かれる | autocomplete-gkill-auth |
| 確認待ちは残っているのに一覧だけが空 | autocomplete-websrv（IDの分割） |
| 期限を跨いだ瞬間に落ちる | autocomplete-gkill-client（`ERR000373`） |
| タグの日時が `0001-01-01` になる | autocomplete-gkill-client（`related_time`） |
| 動画を写真として LLM に渡した | autocomplete-gkill-client（`?thumb=` と `Content-Type`） |
| 解析が終わらない／ErrBadResponse が出続ける | autocomplete-suggest（`maxAnswerTokens` / `response_format`） |
| 解析が遅い／LLM ばかり呼ばれる | autocomplete-suggest（`TierHopeless`） |
| 同じ場所で毎回止まる／タブを閉じたら解析が死んだ | autocomplete-analyze-run |
| 却下した提案がまた出てくる | autocomplete-store, autocomplete-suggest |
| 別のアカウントの判定に上書きされた | autocomplete-store（複合主キー） |
| オーバーレイが見えない／流星が増え続ける | autocomplete-client-pwa |
| ログアウトしても端末に記録が残る | autocomplete-client-pwa（Service Worker） |
| `go build` が no matching files found で落ちる | autocomplete-config-build-docs（`PLACEHOLDER.md`） |
| `npm run verify_docs` が落ちた | autocomplete-config-build-docs |

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

## Things to change together

| 変えたもの | 一緒に直すもの |
|---|---|
| Go の DTO | `src/client/classes/api.ts` |
| API を追加 | `documents/reverse/api-endpoints.md` |
| 設定のキーを追加 | `config.example.json`、`config_test.go` |
| 資料に数値を書いた | `src/tools/verify_docs.mjs` の `buildCountAssertions` |
| スキルを足す/消す/改名 | `AGENTS.md` のルーティング表（`checkSkills` が落ちる） |

## Lint & Code Quality

ESLint flat config（`eslint.config.js`）。対象は `src/client/**`。
`@typescript-eslint/no-explicit-any` は error、`no-unused-vars` は warn。

Go: `slices.SortFunc`（`sort.Slice` は使わない）、`for range n`、`any`（`interface{}` は使わない）、
複数エラーは `errors.Join`。

## Language

コード（変数名・コメント・コミットメッセージ）と資料は日本語。

## Documentation

- 領域別の禁止文・不変条件の正本: `.claude/skills/autocomplete-*/SKILL.md`（上のルーティング表から引く）
- `documents/reverse/` に **22本**（README 含む）。gkill 本体の `documents/reverse/` と**同じ分類**にしてある（行き来しやすくするため）。gkill にあってこちらに無いのは `dvnf-rep-type-spec` / `plugin-system` / `mcp-setup-guide` の3本で、いずれも該当する仕組みを持たないため。索引は [documents/reverse/README.md](documents/reverse/README.md)
- `npm run verify_docs` が件数・リンク・参照パス・Mermaid の種別・架空値・入口のサイズ上限・スキル索引の網羅・個人情報・アンカーコメントを機械検査し、`npm test` に組み込まれている。**数値を書いた資料を増やしたら `buildCountAssertions` に1行足すこと。** 足し忘れると、その資料にだけ古い数値が残り続ける
- 資料層の保守手順は [autocomplete-config-build-docs](.claude/skills/autocomplete-config-build-docs/SKILL.md) スキルにある

**資料に書いてよいのは件数・構造・型であって、記録の中身ではない。** 例示は `SampleRep` / `タグA` 等の架空値だけ。

## AI エージェントへの約束

- **個人情報・実環境の情報をリポジトリへ入れない（最重要）。** 上の「絶対に守ること（個人情報）」が正本。
  実在の利用者ID・人名・メールアドレス・端末のローカル絶対パス・実タグ名・実 rep 名・本文・画像を、
  コード・資料・テストデータ・コミットメッセージのどこにも書かない。例示パスは `$HOME` や
  `〈ユーザー名〉` のプレースホルダで書く。`npm run verify_docs` がパターン検査するが、
  検査は網でしかない — 書く前に止めることがすべて。
- **このファイルと `CLAUDE.md` に領域別の規約本文を書き足さない。** 正本は `.claude/skills/*/SKILL.md`。
  ここが太ると全タスクの常時コンテキストを食う。サイズ上限（verify_docs が検査）に当たったら、
  上限を上げるのではなく中身をスキルへ落とすこと。
- 資料に書いた件数・リンク・ファイル名は `npm run verify_docs`（`npm test` に含まれる）が機械検査する。
  数字を書いたら `src/tools/verify_docs.mjs` の検査にも載せること。
- 作業報告・コミットメッセージ・新規コメントは日本語で書く。
