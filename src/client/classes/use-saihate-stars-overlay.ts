import { onMounted, onUnmounted, ref } from 'vue'

// gkill 本体の星の演出を引き継いだもの。
//
// 本体からの変更点は2つ。
//   - 画面の大きさが変わったら星を置き直す(本体はマウント時の大きさに固定)
//   - 動きを減らす設定の端末では流星を出さない
export function useSaihateStarsOverlay() {
    // ── Template refs ──
    const star_field = ref<HTMLElement | null>(null)

    // 流星ループの再スケジュール用タイマー。
    // テーマの切り替えでこのコンポーネントは作り直されるので、
    // 解除しないとループが積み重なって流星が増え続ける。
    let shooting_star_timer_id: ReturnType<typeof setTimeout> | null = null
    let stopped = false

    const prefers_reduced_motion = (): boolean =>
        typeof window.matchMedia === 'function' &&
        window.matchMedia('(prefers-reduced-motion: reduce)').matches

    // ── Internal helpers ──
    function create_star(class_name: string, top: number, left: number, duration?: number): void {
        const star = document.createElement('div')
        star.className = class_name
        star.style.top = `${top}px`
        star.style.left = `${left}px`
        if (duration) {
            star.style.animationDuration = `${duration}s`
        }
        star_field.value?.appendChild(star)
    }

    function fill_star_field(): void {
        const field = star_field.value
        if (!field) {
            return
        }
        field.replaceChildren()

        const height = window.innerHeight
        const width = window.innerWidth

        for (let i = 0; i < 100; i++) {
            create_star('background-star', Math.random() * height, Math.random() * width, Math.random() * 2 + 1)
        }
        for (let i = 0; i < 5; i++) {
            create_star('background-star red-star', Math.random() * height, Math.random() * width)
            create_star('background-star big-star', Math.random() * height, Math.random() * width)
            create_star('background-star blue-star', Math.random() * height, Math.random() * width)
        }
    }

    function create_shooting_star(): void {
        const star = document.createElement('div')
        star.className = 'shooting-star'
        const length = Math.random() * 100 + 100
        const start_x = Math.random() * window.innerWidth
        const start_y = Math.random() * window.innerHeight * 0.5
        const duration = (Math.random() * 0.5 + 0.5).toFixed(2)

        star.style.width = `${length}px`
        star.style.height = '2px'
        star.style.position = 'absolute'
        star.style.top = `${start_y}px`
        star.style.left = `${start_x}px`
        star.style.transform = 'rotate(135deg)'
        star.style.background = 'linear-gradient(135deg, rgba(255,255,255,0) 0%, rgba(255,255,255,0.6) 50%, white 100%)'
        star.style.animation = `shooting ${duration}s ease-out forwards`
        star.style.pointerEvents = 'none'
        star.style.opacity = '0'
        star_field.value?.appendChild(star)

        setTimeout(() => star.remove(), Number(duration) * 1000)
    }

    function loop_shooting_stars(): void {
        if (stopped) {
            return
        }
        const count = Math.floor(Math.random() * 3) + 1
        for (let i = 0; i < count; i++) {
            setTimeout(create_shooting_star, Math.random() * 300)
        }
        shooting_star_timer_id = setTimeout(loop_shooting_stars, Math.random() * 1500 + 500)
    }

    function on_resize(): void {
        fill_star_field()
    }

    // ── Lifecycle ──
    onMounted(() => {
        fill_star_field()
        window.addEventListener('resize', on_resize)
        if (!prefers_reduced_motion()) {
            loop_shooting_stars()
        }
    })

    onUnmounted(() => {
        stopped = true
        window.removeEventListener('resize', on_resize)
        if (shooting_star_timer_id !== null) {
            clearTimeout(shooting_star_timer_id)
            shooting_star_timer_id = null
        }
    })

    // ── Return ──
    return {
        // Template refs
        star_field,
    }
}
