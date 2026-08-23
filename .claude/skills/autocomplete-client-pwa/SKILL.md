---
name: autocomplete-client-pwa
description: "gkill_autocomplete のフロントエンド（src/client/、Vue 3 + Vuetify 4 + PWA）の約束。gkill のテーマを継承すること、.v-layout--full-height の background-color 0000 が唯一の透過スイッチであること、ダーク時の黒地は Vuetify の color-scheme dark 頼みであること、オーバーレイの style は scoped 不可であること、onUnmounted でタイマーを解除しないと流星が増え続けること、Web フォントは MDI アイコンのみで woff2 に削ること、/api と /thumb は NetworkOnly かつ navigateFallbackDenylist であること、defineProps の props が v-slot activator の props と名前衝突することを扱う。src/client/・vite.config.ts・src/tools/build_icons.mjs・public/ を編集するとき必読。「オーバーレイが見えない」「流星が増え続ける」の調査でも必読。"
---

# フロントエンドと PWA の不変条件

対象: `src/client/**` / `vite.config.ts` / `src/tools/build_icons.mjs` / `public/**`

**このファイルは全文が、破ると静かに壊れる約束である。該当作業では飛ばさずに読むこと。**

## GUI

Vue 3.5 + Vuetify 4.1 + Vite 8 + TypeScript 6（gkill と同一メジャー）。**gkill のテーマを継承する**：ダークは星が降り、ライトは雪が降る。

移植で必ず踏む穴：

1. **`.v-layout--full-height { background-color: #0000 }` が唯一の透過スイッチ**。これが無いとオーバーレイは一切見えない
2. ダーク時の黒地は Vuetify が注入する `:root { color-scheme: dark }` 頼み
3. オーバーレイの `<style>` は **scoped 不可**（動的生成 DOM に scope 属性が付かない）
4. **`onUnmounted` でタイマー解除が必須**。テーマ切替が `v-if` なので、解除しないとループが多重化して流星が増え続ける

**Web フォントは MDI アイコンのみ**。テキスト用の Web フォントは読み込まない（gkill もそうで、実効は Vuetify 既定の `Roboto, sans-serif`）。MDI は woff2 のみに削る Vite プラグインを入れる。

## PWA

`vite-plugin-pwa`。アイコンは `src/tools/build_icons.mjs` が依存なしで描き、生成物を `public/` に置いて追跡する（毎回のビルドで作り直さない）。

**`/api` と `/thumb` は `NetworkOnly` かつ `navigateFallbackDenylist`。** 前者は記録の中身を端末に残さないため、後者は API の失敗が「壊れた JSON」として現れて原因が追えなくなるのを防ぐため。

**Service Worker に記録を溜めさせない。** 溜めてよいのは画面の器（JS/CSS/フォント/アイコン）だけ。
溜めるとログアウトしても端末に残る。

テンプレートで `defineProps` の結果を `props` という名前にすると、`v-slot:activator="{ props }"` と**名前が衝突する**。スロット側を `{ props: tooltip_props }` に改名すること。

## 画面から出る通信

`onUnmounted` で購読・タイマーを必ず解除する。却下すると次から提案されなくなる旨は画面で伝える。

## 関連スキル

- [autocomplete-websrv](../autocomplete-websrv/SKILL.md) — 画面が叩く自前 API
- [autocomplete-config-build-docs](../autocomplete-config-build-docs/SKILL.md) — ビルドと embed
