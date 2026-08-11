import { onMounted, onUnmounted, ref } from 'vue'

// gkill 本体の雪の演出を引き継いだもの。
//
// 本体からの変更点は1つ。動きを減らす設定の端末では雪を降らせない。
export function useSnowFallOverlay() {
    // ── Template refs ──
    const snow_field = ref<HTMLElement | null>(null)

    // テーマの切り替えでこのコンポーネントは作り直されるので、
    // 解除しないとループが積み重なって雪の量が増え続ける。
    let timer_id: ReturnType<typeof setTimeout> | null = null
    let stopped = false

    const prefers_reduced_motion = (): boolean =>
        typeof window.matchMedia === 'function' &&
        window.matchMedia('(prefers-reduced-motion: reduce)').matches

    // ── Internal helpers ──
    function create_snowflake(): void {
        const flake = document.createElement('div')
        flake.className = 'snowflake'

        const size = Math.random() * 6 + 2
        const left = Math.random() * window.innerWidth
        const duration = Math.random() * 5 + 5

        flake.style.width = `${size}px`
        flake.style.height = `${size}px`
        flake.style.left = `${left}px`
        flake.style.animationDuration = `${duration}s`

        snow_field.value?.appendChild(flake)

        setTimeout(() => flake.remove(), duration * 1000)
    }

    function loop_snowfall(): void {
        if (stopped) {
            return
        }
        create_snowflake()
        timer_id = setTimeout(loop_snowfall, 100)
    }

    // ── Lifecycle ──
    onMounted(() => {
        if (!prefers_reduced_motion()) {
            loop_snowfall()
        }
    })

    onUnmounted(() => {
        stopped = true
        if (timer_id !== null) {
            clearTimeout(timer_id)
            timer_id = null
        }
    })

    // ── Return ──
    return {
        // Template refs
        snow_field,
    }
}
