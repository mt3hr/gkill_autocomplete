import { fileURLToPath, URL } from 'node:url'
import { defineConfig, type Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'
import vuetify from 'vite-plugin-vuetify'
import { VitePWA } from 'vite-plugin-pwa'

/**
 * Material Design Icons の Web フォントを woff2 だけに絞る。
 *
 * @mdi/font の CSS は eot/woff2/woff/ttf の4形式を並べているので、
 * 何もしないと4ファイル(計3.6MB)が出力される。確認画面を開くのは
 * 手元のブラウザだけなので woff2 で足りる。
 * url() の参照を消せば Vite はそのファイルを出力しない。
 *
 * gkill 本体の同名プラグインを引き継いだもの。
 */
function mdiWoff2Only(): Plugin {
    return {
        name: 'gkill-autocomplete-mdi-woff2-only',
        enforce: 'pre',
        transform(code: string, id: string) {
            if (!id.includes('@mdi/font') || !id.endsWith('.css')) {
                return null
            }
            const replaced = code
                .replace(/src:\s*url\([^)]*\.eot[^)]*\);/g, '')
                .replace(/url\([^)]*\.eot[^)]*\)\s*format\(["']embedded-opentype["']\),?\s*/g, '')
                .replace(/,?\s*url\([^)]*\.woff\?[^)]*\)\s*format\(["']woff["']\)/g, '')
                .replace(/,?\s*url\([^)]*\.ttf[^)]*\)\s*format\(["']truetype["']\)/g, '')
            return replaced === code ? null : { code: replaced, map: null }
        },
    }
}

/**
 * 記録の中身に触れる経路。**決してキャッシュしない。**
 *
 * この画面には未確認の提案、つまり利用者の生活の記録がそのまま並ぶ。
 * Service Worker が応答を溜め込むと、記録の本文と写真がブラウザの
 * キャッシュとしてディスクに残る。ログアウトしても残るし、
 * 端末を共有していれば他の人からも読める。
 *
 * 溜めてよいのは画面そのもの(JS/CSS/フォント)だけ。
 */
const NEVER_CACHED_PATHS = /^\/(api|thumb)(\/|$|\?)/

export default defineConfig({
    plugins: [
        mdiWoff2Only(),
        vue(),
        // テンプレートで実際に使っている Vuetify のコンポーネントだけを取り込む。
        vuetify({ autoImport: true }),
        VitePWA({
            // 新しい版が出ていたら黙って入れ替える。
            // 確認画面は自分で建てて自分で使うものなので、更新を尋ねる意味がない。
            registerType: 'autoUpdate',
            injectRegister: 'auto',
            // 開発中に Service Worker が挟まると、変更が反映されない原因が分かりにくくなる。
            devOptions: { enabled: false },
            includeAssets: ['favicon.png', 'apple-touch-icon.png'],
            manifest: {
                name: 'gkill タグ提案',
                short_name: 'タグ提案',
                description: 'gkill の記録に付けるべきタグを提案します。承認したものだけが書き込まれます。',
                lang: 'ja',
                start_url: '/',
                scope: '/',
                display: 'standalone',
                orientation: 'portrait',
                background_color: '#ffffff',
                theme_color: '#2672ed',
                icons: [
                    { src: 'pwa-192x192.png', sizes: '192x192', type: 'image/png' },
                    { src: 'pwa-512x512.png', sizes: '512x512', type: 'image/png' },
                    {
                        src: 'pwa-maskable-512x512.png',
                        sizes: '512x512',
                        type: 'image/png',
                        purpose: 'maskable',
                    },
                ],
            },
            workbox: {
                // 画面の器だけを溜める。記録に触れるものは1つも入れない。
                globPatterns: ['**/*.{js,css,html,woff2,png,svg}'],
                // 画面が無い経路へ index.html を返さない。
                // 返すと、API の失敗が「壊れた JSON」として現れて原因が追えなくなる。
                navigateFallback: 'index.html',
                navigateFallbackDenylist: [NEVER_CACHED_PATHS],
                runtimeCaching: [
                    {
                        // 明示的に素通しにする。既定でも溜めないが、
                        // ここに書いておけば「溜めない」が意図だと分かる。
                        urlPattern: NEVER_CACHED_PATHS,
                        handler: 'NetworkOnly',
                    },
                ],
                // 古い版の Service Worker が残って画面だけ古いままになるのを防ぐ。
                cleanupOutdatedCaches: true,
                clientsClaim: true,
                skipWaiting: true,
            },
        }),
    ],
    resolve: {
        alias: {
            '@': fileURLToPath(new URL('./src/client', import.meta.url)),
        },
    },
    server: {
        // 開発中も自分のサーバへ繋ぐ。既定の待ち受けに合わせてある。
        proxy: {
            '/api': 'http://127.0.0.1:9797',
            '/thumb': 'http://127.0.0.1:9797',
        },
    },
    build: {
        outDir: 'dist',
        emptyOutDir: true,
    },
})
