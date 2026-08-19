# フォルダ構成

どこに何があるかを示します。件数は `npm run verify_docs -- --list` が算出した実測値です。

## プロジェクトルート

```
gkill_autocomplete/
├── README.md                使い方の入口
├── CLAUDE.md                開発支援ツール向けの指針
├── LICENSE                  MIT
├── package.json             ビルドとテストの入口
├── config.example.json      設定の雛形(値はすべて架空)
├── index.html               確認画面の器
├── vite.config.ts           確認画面のビルド設定
├── eslint.config.js         確認画面の静的検査
├── tsconfig*.json           TypeScript の設定
├── public/                  PWA のアイコン(build_icons.mjs が生成、追跡する)
├── documents/reverse/       設計資料(22ファイル)
└── src/
    ├── autocomplete/        Go の本体
    ├── client/              確認画面(Vue)
    └── tools/               ビルドと検査のスクリプト
```

## 設計資料の構成

gkill 本体の `documents/reverse/` と同じ分類にしてあります。行き来しやすくするためです。

| 分類 | 資料 |
| --- | --- |
| 基盤 | [glossary.md](glossary.md)、[design-philosophy.md](design-philosophy.md)、[folder-structure.md](folder-structure.md) |
| 何をするか | [requirements.md](requirements.md)、[usecase.md](usecase.md) |
| 構造 | [er-diagram.md](er-diagram.md)、[class-diagrams.md](class-diagrams.md)、[program-spec.md](program-spec.md) |
| 動き | [sequence-diagrams.md](sequence-diagrams.md)、[activity-diagrams.md](activity-diagrams.md)、[state-machines.md](state-machines.md)、[scenario.md](scenario.md) |
| 画面 | [screen-transition.md](screen-transition.md)、[screen-specs.md](screen-specs.md)、[frontend-architecture.md](frontend-architecture.md) |
| 境界 | [api-endpoints.md](api-endpoints.md)、[error-handling-and-security.md](error-handling-and-security.md) |
| 手順 | [dev-setup.md](dev-setup.md)、[testing-guide.md](testing-guide.md)、[operations-guide.md](operations-guide.md) |
| 利用者向け | [user-guide.md](user-guide.md) |

読み順と依存関係は [README.md](README.md) にあります。

**gkill 本体にあってこちらに無いもの**: `dvnf-rep-type-spec.md`（gkill 固有の命名規則）、
`plugin-system.md`（プラグイン機構を持たない）、`mcp-setup-guide.md`（MCP サーバを持たない）。

## `src/autocomplete/` — Go の本体

module は `github.com/mt3hr/gkill_autocomplete/src/autocomplete`。`go.mod` はここに置きます。
Go ファイルは **45ファイル**（うちテスト **18ファイル**）です。

```
src/autocomplete/
├── go.mod / go.sum
├── cmd/gkill_autocomplete/  唯一のバイナリの入口
└── internal/                11パッケージ
    ├── app/         全体の配線。解析と設定生成
    ├── classify/    LLM に候補タグの当否を尋ねる
    ├── config/      設定の読み込みと検証。外部送信の拒否はここ
    ├── embed/       確認画面をバイナリに埋め込む
    ├── gkillauth/   gkill の設定ディレクトリを使った認証
    ├── gkillclient/ 稼働中の gkill への HTTP クライアント
    ├── ids/         決定的な識別子
    ├── llm/         ローカル LLM への問い合わせ
    ├── store/       提案と人間の判定の保存
    ├── suggest/     学習と判定
    └── websrv/      確認画面の配信と自前の API
```

依存の向きは一方向です。`suggest` は `config` と `gkillclient` にしか依存せず、
`llm` や `websrv` を知りません。LLM を差し替えても `suggest` は変わりません。

```
cmd ──→ app ──→ suggest ──→ gkillclient ──→ (gkill)
         │        │              ↑
         │        └──→ config    │ SessionSource
         ├──→ store ──→ ids      │
         ├──→ classify ──→ llm ──→ (ローカル LLM)
         └──→ websrv ──→ embed
                  │
cmd ──→ gkillauth ┘ ──→ (gkill のソース: dao/account, dao/account_state, dao/server_config)
```

`gkillauth` だけが gkill 本体のコードを import します。パスワードの照合も
セッションの発行も、gkill 自身の実装をそのまま使います。**Argon2id のパラメータや
比較の仕方を写し取ると、ずれたときに静かに弱くなる**ためです。

`gkillclient` は「どうやってセッションを手に入れるか」を知りません。
`SessionSource` インターフェース越しに受け取るので、この層は認証の仕組みから独立しています。

### `internal/embed/html/`

確認画面のビルド成果物の置き場です。中身は追跡しませんが、**`PLACEHOLDER.md` だけは追跡します**。
ディレクトリが空になると `//go:embed` がコンパイルエラーになるためです。

## `src/client/` — 確認画面

Vue 3 + Vuetify 4。TypeScript と Vue のファイルで **14ファイル**です。

```
src/client/
├── main.ts                        起動
├── App.vue                        器。テーマの切り替えと降りものの出し分け
├── env.d.ts
├── plugins/vuetify.ts             配色(gkill 本体から引き継ぎ)
├── classes/
│   ├── api.ts                     自分のサーバへの問い合わせ
│   ├── theme.ts                   テーマの記憶(クッキー)
│   ├── use-tag-suggestion-page.ts 確認画面の動き
│   ├── use-saihate-stars-overlay.ts 星(ダークテーマ)
│   └── use-snow-fall-overlay.ts     雪(ライトテーマ)
└── pages/
    ├── tag-suggestion-page.vue    確認画面
    └── views/
        ├── login-view.vue         gkill のアカウントで入る
        ├── attached-tag.vue       付いているタグ(gkill 本体と同じ見た目)
        ├── saihate-stars-overlay.vue
        └── snow-fall-overlay.vue
```

PWA です。ホーム画面に入れて単体のアプリのように開けます。
**Service Worker が溜めるのは画面の器(JS/CSS/フォント/アイコン)だけで、
`/api` と `/thumb` は決してキャッシュしません**。記録の中身を端末に残さないためです。
そのぶんオフラインでは画面が開くだけで中身は出ませんが、これは意図した動きです。

見た目は gkill 本体から引き継いでいます。ダークテーマでは星が降り、ライトテーマでは雪が降ります。
配色・スクロールバー・通知の出し方も揃えてあります。詳しくは [program-spec.md](program-spec.md) を参照してください。

## `src/tools/`

依存を持たない Node のスクリプトを置きます。

```
src/tools/
├── build_go.mjs      クロスコンパイル
├── build_icons.mjs   PWA のアイコンを描く(依存なし。生成物は public/ に置いて追跡する)
└── verify_docs.mjs   資料の件数とリンクの検査
```

## 実行時に作られるもの（リポジトリの外）

設定とデータベースは `$GKILL_AUTOCOMPLETE_HOME`（既定 `$HOME/gkill_autocomplete`）
に作られます。**リポジトリの中には決して作りません。**

```
$HOME/gkill_autocomplete/
├── config.json                  設定。実在のタグ名とリポジトリ名が入る
└── gkill_autocomplete.db        提案と人間の判定(利用者ごとに分かれている)
```

いずれも所有者だけが読める権限で作ります。

**資格情報を保存するファイルはありません。** 認証は gkill 自身の設定ディレクトリ
（`$GKILL_HOME/configs/`）を見て行うので、パスワードもセッションも持ちません。

## ビルド成果物

```
dist/                                    確認画面のビルド結果
src/autocomplete/internal/embed/html/    dist/ をコピーしたもの
release/                                 クロスコンパイルしたバイナリ
```

すべて追跡対象外です。
