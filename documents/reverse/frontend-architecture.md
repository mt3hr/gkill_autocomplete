# フロントエンド設計ガイド

確認画面（`src/client/`）の作りです。gkill 本体の流儀を引き継いでいます。

## 1. 構成

Vue 3.5 + Vuetify 4.1 + Vite 8 + TypeScript 6（gkill 本体と同一メジャー）。

```
src/client/
├── main.ts                          起動
├── App.vue                          器
├── env.d.ts
├── plugins/vuetify.ts               配色
├── classes/
│   ├── api.ts                       自分のサーバへの問い合わせ
│   ├── theme.ts                     テーマの記憶(クッキー)
│   ├── use-tag-suggestion-page.ts   確認画面の動き
│   ├── use-saihate-stars-overlay.ts 星
│   └── use-snow-fall-overlay.ts     雪
└── pages/
    ├── tag-suggestion-page.vue      確認画面
    └── views/
        ├── login-view.vue
        ├── attached-tag.vue
        ├── saihate-stars-overlay.vue
        └── snow-fall-overlay.vue
```

**ルータはありません。** 画面が1つしかないので、出し分けは状態で行います。

**状態管理の仕組みも入れていません。** props と emit だけで足ります
（gkill 本体も Pinia / Vuex を使っていません）。

## 2. コンポーザブルに寄せる

`.vue` は見た目だけを持ち、動きは `classes/use-*.ts` に置きます。
gkill 本体と同じ形です。

```
tag-suggestion-page.vue  ──使う──▶  use-tag-suggestion-page.ts  ──使う──▶  api.ts
```

`useTagSuggestionPage` が返すのは、状態（`records` / `focused_index` /
`selected_tags` …）と操作（`load` / `confirm` / `reject` / `toggle_tag` …）です。

この形にしておくと、画面の動きをテンプレートを描かずに検査できます。

## 3. 命名

gkill 本体の規約に合わせています。

| 対象 | 形 | 例 |
| --- | --- | --- |
| データのプロパティ・ローカル変数・通常の関数 | snake_case | `focused_index`、`add_manual_tag` |
| コンポーザブル | `useXxx` | `useTagSuggestionPage` |
| イベントのコールバック | `onXxx` | `on_keydown` は例外的に snake（画面内の私的な関数） |
| 型 | PascalCase | `SuggestionRecord` |
| ファイル | `{動作}-{対象}-{種別}` の kebab-case | `tag-suggestion-page.vue` |

snake_case を使うのは、**Go 側の JSON タグとそのまま対応させる**ためです。
`target_id` / `related_time` / `existing_tags` は Go の構造体タグと同じ綴りです。

## 4. サーバとの通信

`classes/api.ts` に閉じています。相手は同じプロセスなので、接続先の設定はありません。

```ts
post<T>(path, body)   // 共通の POST
  ├─ 401 → UnauthorizedError を投げる
  ├─ それ以外の失敗 → Error を投げる
  └─ 成功 → 応答を返す
```

**`UnauthorizedError` を別の型にしているのが要点です。** 画面はこれを見て
「通知を出す」ではなく「ログイン画面へ戻す」を選びます。期限切れのときに
赤い通知だけ出しても、利用者は何をすればよいか分かりません。

### パスワードの扱い

```ts
async function password_sha256(password: string): Promise<string>
```

平文パスワードの SHA-256 を小文字16進64桁にします。gkill の画面と同じ形で、
**平文はブラウザから出ません**。

`crypto.subtle` は安全なコンテキスト（`https://` か `localhost`）でしか使えません。
使えない場合は**平文を送るのではなく、理由を出して止めます**。

## 5. 一覧の書き戻し方

確定したとき、**押した瞬間に一覧から外し、失敗したら元の位置へ戻します**。

```ts
const removed_index = focused_index.value
records.value = records.value.filter((item) => item.target_id !== record.target_id)
// … 送信 …
// 失敗したら removed_index の位置へ差し戻す
```

待たせないためです。成功が普通で、失敗は稀なので、成功を前提に描いておいて
失敗したときだけ巻き戻します。

二重確定は `in_flight`（`Set<string>`）で防ぎます。押しっぱなしや
連打で同じ記録が2回送られると、2回目は「提案が見つかりません」で失敗します。

## 6. Set を書き換えない

```ts
// 効かない
selected_tags.value.add(tag)

// 正しい
const next = new Set(selected_tags.value)
next.add(tag)
selected_tags.value = next
```

`ref` が持つ `Set` は中身を書き換えても再描画されません。**入れ替えます。**

## 7. キー操作

`window` に `keydown` を1つ張ります。反応しない条件を先に弾くのが要点です。

```ts
if (event.isComposing || event.repeat || event.ctrlKey || event.metaKey || event.altKey) return
if (is_typing(event)) return   // input / textarea / contenteditable
if (!focused_record.value) return
```

**`onUnmounted` で必ず外します。** 外さないと、画面を離れたあとも
キーを拾い続けます。

## 8. 降りもの（星と雪）

gkill 本体から引き継いだ背景です。実装上の落とし穴が4つあります。

| 穴 | 対処 |
| --- | --- |
| Vuetify が `.v-application` に地の色を塗る | `.v-layout--full-height { background-color: #0000 }`。**これが唯一の透過スイッチ** |
| ダーク時の黒地 | Vuetify が注入する `:root { color-scheme: dark }` に任せる |
| 動的に生成する DOM に scope 属性が付かない | オーバーレイの `<style>` は **scoped 不可** |
| テーマ切替が `v-if` なのでループが多重化する | `onUnmounted` でタイマーを解除する。しないと流星が増え続ける |

## 9. Web フォント

**MDI アイコンだけです。** テキスト用の Web フォントは読み込みません
（gkill 本体もそうで、実効は Vuetify 既定の `Roboto, sans-serif`）。

`@mdi/font` の CSS は eot / woff2 / woff / ttf の4形式を並べているので、
何もしないと4ファイル（計3.6MB）が出ます。`vite.config.ts` の
`mdiWoff2Only` プラグインが `url()` の参照を消して **woff2 だけ**にします。
参照が消えれば Vite はそのファイルを出力しません。

## 10. PWA

`vite-plugin-pwa`（`generateSW`）を使います。

### 溜めてよいものと、いけないもの

| 対象 | 扱い | 理由 |
| --- | --- | --- |
| JS / CSS / フォント / アイコン / HTML | 事前に溜める | 画面の器。何度も取り直す意味がない |
| `/api/*` と `/thumb` | **`NetworkOnly`** | 記録の中身。溜めると端末に残る |

```ts
const NEVER_CACHED_PATHS = /^\/(api|thumb)(\/|$|\?)/
```

同じパターンを `navigateFallbackDenylist` にも入れます。入れないと、
API の失敗に `index.html` が返り、**「壊れた JSON」として現れて原因が追えなくなります**。

### 更新のしかた

`registerType: 'autoUpdate'` で、新しい版が出ていたら黙って入れ替えます。
確認画面は自分で建てて自分で使うものなので、更新を尋ねる意味がありません。

`cleanupOutdatedCaches` / `clientsClaim` / `skipWaiting` を有効にして、
**古い Service Worker が残って画面だけ古いまま**になるのを防ぎます。

### 開発中は無効

`devOptions: { enabled: false }`。Service Worker が挟まると、
変更が反映されない原因が分かりにくくなります。

## 11. アイコン

`src/tools/build_icons.mjs` が依存なしで PNG を描き、`public/` に置きます。
**生成物は追跡します**（毎回のビルドで作り直さない。同じ入力から同じ絵しか出ないため）。

RGBA のビットマップを組み立てて zlib で PNG に符号化しています。
`maskable` 版は絵を内側 60% に収めます（Android がアイコンを円などに切り抜くため）。

## 12. テンプレートでの名前の衝突

`defineProps` の結果を `props` という名前で受けると、Vuetify のスロットと衝突します。

```vue
<!-- 衝突する -->
<v-tooltip><template v-slot:activator="{ props }">…{{ props.user_id }}…</template></v-tooltip>

<!-- スロット側を改名する -->
<v-tooltip><template v-slot:activator="{ props: tooltip_props }">…</template></v-tooltip>
```

## 13. 静的検査

ESLint 10（flat config）。**`no-explicit-any` はエラー**です。
`unknown` か具体的な型を使います。

```
npm run lint         自動修正つき
npm run type-check   vue-tsc
```

型検査とビルドは `npm run build` で並行に走ります。
