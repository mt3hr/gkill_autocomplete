<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useTheme } from 'vuetify'
import SaihateStarsOverlay from './pages/views/saihate-stars-overlay.vue'
import SnowFallOverlay from './pages/views/snow-fall-overlay.vue'
import LoginView from './pages/views/login-view.vue'
import TagSuggestionPage from './pages/tag-suggestion-page.vue'
import { get_use_dark_theme } from './classes/theme'
import { fetch_session } from './classes/api'

const theme = useTheme()
theme.global.name.value = get_use_dark_theme() ? 'gkill_dark_theme' : 'gkill_theme'

// ログイン状態が分かるまでは、どちらの画面も出さない。
// 先に本体を描いてしまうと、未ログインでも中身があるように見えてしまう。
const is_checked = ref(false)
const is_authenticated = ref(false)
const logged_in_user_id = ref('')
// is_analyzable が偽なら、ログインはできても提案が無い。
// 起動時に --user へ渡されなかったアカウントがこれに当たる。
const is_analyzable = ref(false)

async function refresh_session(): Promise<void> {
    const session = await fetch_session()
    is_authenticated.value = session.authenticated
    logged_in_user_id.value = session.user_id
    is_analyzable.value = session.analyzable
    is_checked.value = true
}

onMounted(() => {
    void refresh_session()
})

function on_logged_in(): void {
    // 誰として入ったか、その人が解析対象かをサーバに聞き直す。
    // ログインの応答だけでは解析対象かどうかが分からない。
    void refresh_session()
}

function on_logged_out(): void {
    is_authenticated.value = false
    logged_in_user_id.value = ''
    is_analyzable.value = false
}
</script>

<template>
    <div>
        <!-- ダークなら星が降り、ライトなら雪が降る。gkill 本体と同じ。 -->
        <SaihateStarsOverlay v-if="theme.global.name.value === 'gkill_dark_theme'" />
        <SnowFallOverlay v-if="theme.global.name.value === 'gkill_theme'" />
        <v-app>
            <template v-if="is_checked">
                <TagSuggestionPage v-if="is_authenticated" :user_id="logged_in_user_id"
                    :is_analyzable="is_analyzable" @logged_out="on_logged_out()" />
                <LoginView v-else @logged_in="on_logged_in()" />
            </template>
        </v-app>
    </div>
</template>

<style lang="css">
/*
  これが降りものを見せるための唯一の仕掛け。
  Vuetify は .v-application に地の色を塗るので、透明にしないと
  背後のオーバーレイ(z-index: -100000000)が完全に隠れてしまう。
*/
.v-layout--full-height {
    background-color: #0000;
}

html {
    overflow-y: hidden !important;
}

body {
    overflow-y: hidden !important;
}

body::-webkit-scrollbar {
    display: none;
}

/* スクロールバー。gkill 本体と同じくテーマ色に追従させる。 */
:root {
    --gkill-scrollbar-size: 8px;
    --gkill-scrollbar-thumb-width: 6px;
    --gkill-scrollbar-thumb-radius: 5px;
}

*::-webkit-scrollbar {
    margin-left: 1px;
    width: var(--gkill-scrollbar-size);
    height: var(--gkill-scrollbar-size);
}

*::-webkit-scrollbar-thumb {
    background: rgb(var(--v-theme-primary));
    width: var(--gkill-scrollbar-thumb-width);
    border-radius: var(--gkill-scrollbar-thumb-radius);
}

/* 通知。右上に積む。 */
.alert_container {
    justify-items: end;
    position: fixed;
    top: 60px;
    right: 10px;
    display: grid;
    grid-gap: 0.5em;
    z-index: 100000000;
}

.alert_container>div {
    width: fit-content;
}

h1,
h2 {
    margin: 0px;
}
</style>
