---
name: autocomplete-config-build-docs
description: "gkill_autocomplete の設定（src/autocomplete/internal/config/ と config.example.json）・ビルド・資料層の約束。LLM はループバック限定で allow_remote が無ければ起動を拒否しクラウド LLM の API キー項目を設定スキーマに用意しないこと、設定に資格情報の項目を作らないこと（config_test.go が機械検査）、ScopeConfig を必ず絞り実在のリポジトリ名を既定値に焼き込まないこと、保存時に Redacted を通さないこと、embed の PLACEHOLDER.md を追跡すること、資料に数値を書いたら buildCountAssertions へ1行足すこと、スキルの保守手順を扱う。internal/config/・config.example.json・package.json・src/tools/・documents/・AGENTS.md・.claude/skills/ を編集するとき、verify_docs が落ちたとき必読。"
---

# 設定・ビルド・資料層の不変条件

対象: `src/autocomplete/internal/config/**` / `config.example.json` / `package.json` /
`src/tools/**` / `documents/**` / `AGENTS.md` / `CLAUDE.md` / `.claude/skills/**` /
`.gitignore` / `.gitattributes`

**このファイルは全文が、破ると静かに壊れる約束である。該当作業では飛ばさずに読むこと。**

## 設定は個人情報の要

- **LLM はループバック限定**。`llm.endpoint` が 127.0.0.1 / ::1 / localhost 以外なら、`allow_remote: true` が明示されていない限り**起動を拒否する**。クラウド LLM の API キー項目は設定スキーマに**用意しない**（書けなければ事故が起きない）
- **設定に資格情報の項目を作らない**。認証は gkill の設定ディレクトリを直接見るので、パスワードもハッシュもセッションも持つ必要がない。`config_test.go` が「JSON に password / credential / session を含むキーが無い」ことを機械検査する
- **通信先は gkill とローカル LLM の2つだけ**。テレメトリ・クラッシュレポート・更新チェックを入れない
- **ログに本文を出さない**。既定は ID・件数・所要時間まで。本文・ファイル名・LLM のプロンプトは出さない

### ScopeConfig は必ず絞る

```go
// ScopeConfig は解析の対象範囲。
//
// 無制限に走らせると他のツールが自動収集した記録まで拾ってしまうので必ず絞る。

	// RepPrefixes が空のときは「手作業でタグを付けている割合が高いリポジトリ」を
	// 自動で選ぶ。実在のリポジトリ名を既定値としてリポジトリに焼き込まないための既定。
```

gkill 側の設定を手で書くと食い違って繋がらなくなる。

### 保存時に Redacted を通さない

```go
// Redacted は通さない。ここで書くのは利用者自身の設定ファイルであり、
// 伏せ字を書き込んでしまうと動かなくなる。
```

伏せ字（`Redacted`）は**表示のためのもの**。書き戻す経路に混ぜないこと。

## ビルド

| Command | Purpose |
|---|---|
| `npm run install_app` | フル build（フロント → embed → `go install`） |
| `npm run go_install` | Go のみ（フロント再ビルドなし） |
| `npm run go_mod` | go.mod / go.sum を作り直す |
| `npm run vet` | `go vet ./...` |
| `npm run build` | フロントのビルド（型検査 + vite build） |
| `npm run lint` | ESLint（自動修正あり） |
| `npm run verify_docs` | 資料の検査。`--list` で実測値を出す |
| `npm test` | `verify_docs` → Go テスト → フロントのユニットテスト |
| `npm run release` | クロスコンパイル |

**Go module root**: `src/autocomplete`（`go.mod` はここ）。module path は `github.com/mt3hr/gkill_autocomplete/src/autocomplete`。CGO 不要（pure Go SQLite）。

**embed**: `src/autocomplete/internal/embed/` はビルド成果物の置き場。中身は gitignore しつつ **`PLACEHOLDER.md` だけ追跡する**。空にすると `//go:embed` がコンパイルエラーになる。

**`.gitattributes`** は `core.autocrlf` に任せず `* text=auto eol=lf`。`.ps1` / `.bat` / `.cmd` は CRLF、`.sh` は LF。

## 資料の層

- 領域別の禁止文・不変条件の**正本**: `.claude/skills/autocomplete-*/SKILL.md`
- 全タスクで要る核とルーティング表: `AGENTS.md`
- **現在どうなっているか**（What）: `documents/reverse/` に **22本**（README 含む）。gkill 本体の
  `documents/reverse/` と**同じ分類**にしてある（行き来しやすくするため）。gkill にあってこちらに
  無いのは `dvnf-rep-type-spec` / `plugin-system` / `mcp-setup-guide` の3本で、いずれも該当する
  仕組みを持たないため
- `CLAUDE.md` と他AI入口3本は導線だけ。規約本文ゼロ

`npm run verify_docs` が件数・リンク・参照パス・Mermaid の種別・架空値・入口のサイズ上限・
スキル索引の網羅・個人情報・アンカーコメント・`.gitignore` の形を機械検査し、`npm test` に
組み込まれている。**数値を書いた資料を増やしたら `buildCountAssertions` に1行足すこと。**
足し忘れると、その資料にだけ古い数値が残り続ける。

**資料に書いてよいのは件数・構造・型であって、記録の中身ではない。** 例示は `SampleRep` / `タグA` 等の架空値だけ。
`checkFictionalExamples` が `種別_端末_YYYYMMDD` 形の実在リポジトリ名の混入を検出する。

**ADR とマニュアルの検査は移植していない。** このリポジトリに documents/adr/ もマニュアルも無く、
実体の無い検査は空回りするため。作るときに gkill 本体から移植する。

## スキルの保守手順

- スキルを足す/消す/改名したら `AGENTS.md` のルーティング表を**同じコミットで**更新する
  （`checkSkills` が双方向網羅を検査して落とす）
- frontmatter は `name`（ディレクトリ名と完全一致）と `description`
  （**二重引用符でくくった1物理行**・80〜1024字・パスやファイル名を含む）だけ
- **1スキル = SKILL.md 1ファイル。** 補助 `.md` を置かない
- スキルに Mermaid を書かない（`checkMermaid` の対象は `documents/reverse/` だけ）
- スキル間リンクは `../<name>/SKILL.md`、資料へは `../../../documents/...`（3階層固定）
- **`AGENTS.md` / `CLAUDE.md` に領域別の規約本文を書き足さない。** サイズ上限に当たったら、
  **上限を上げるのではなく中身をスキルへ落とす**
- 高リスクなソースの先頭には
  `// 編集前に読む: .claude/skills/<name>/SKILL.md（この領域の不変条件の正本）` を置く。
  参照先の実在は `checkSkillAnchors` が検査する
- `.gitignore` の `/.claude/*` + `!/.claude/skills/` の2行組を戻さない。裸の `.claude/` に戻すと
  git はネガティブパターンで親ディレクトリの除外を打ち消せないため skills が追跡されず、
  **ローカルではファイルが残るので気づけない**（`checkGitignoreSkills` が先に止める）
- 個人情報（実在の利用者ID・人名・メールアドレス・端末のローカル絶対パス）を資料へ書かない。
  固有の NG 語は `verify_docs_personal_ngwords.local.txt`（gitignore 済み・**コミットしない**）へ

**`verify_docs.mjs` 自身に単体テストは無い。** 検査を変えたら、わざと壊して落ちることを手で確認する。

## 関連スキル

- [autocomplete-gkill-auth](../autocomplete-gkill-auth/SKILL.md) — 資格情報を設定に持たない理由
- [autocomplete-suggest](../autocomplete-suggest/SKILL.md) — LLM 設定の効き先
- [autocomplete-client-pwa](../autocomplete-client-pwa/SKILL.md) — フロントのビルドと PWA
