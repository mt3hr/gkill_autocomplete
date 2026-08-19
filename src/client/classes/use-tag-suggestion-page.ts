import { computed, onMounted, onUnmounted, ref, type Ref } from 'vue'
import { useTheme } from 'vuetify'
import {
    decide,
    fetch_analyze_status,
    fetch_suggestions,
    logout,
    start_analyze,
    UnauthorizedError,
    type AnalyzeStatus,
    type SuggestionRecord,
} from './api'
import { set_use_dark_theme } from './theme'

type AlertMessage = {
    id: number
    text: string
    is_error: boolean
}

// 通知の自動消滅までの時間。gkill 本体と同じ長さにしてある。
const alert_auto_close_milli_seconds = 2500

// 解析の進み具合を見に行く間隔。
//
// 写真1件の判定は数分かかることがあるので、細かく叩いても意味が無い。
const analyze_poll_interval_milli_seconds = 2000

// on_unauthorized は期限切れなどでログインが要るようになったときに呼ばれる。
export function useTagSuggestionPage(on_unauthorized: () => void) {
    const theme = useTheme()

    // ── State ──
    const records: Ref<SuggestionRecord[]> = ref([])
    const focused_index = ref(0)
    // selected_tags は「いま承認しようとしているタグ」。
    // 記録ごとに作り直す。0個で確定すると「タグは要らない」になる。
    const selected_tags: Ref<Set<string>> = ref(new Set())
    const manual_tag = ref('')

    const is_loading = ref(false)
    const is_analyzing = ref(false)
    const is_deciding = ref(false)
    const pending_count = ref(0)
    // skipped_count は「中身を取りに行ったのに gkill から返ってこなかった」記録の数。
    // 一覧が空のときの言い分けに使う。
    const skipped_count = ref(0)
    const messages: Ref<AlertMessage[]> = ref([])

    // analyze_done / analyze_total は解析の進み具合。
    // 総数が決まる前は 0 / 0 になる。
    const analyze_done = ref(0)
    const analyze_total = ref(0)

    let next_message_id = 0
    // in_flight は同じ記録への二重確定を防ぐ。
    const in_flight = new Set<string>()

    // analyze_poll_timer_id は進み具合を見に行くタイマー。
    // **onUnmounted で必ず解除する。** 残すと画面を離れても叩き続ける。
    let analyze_poll_timer_id: ReturnType<typeof setTimeout> | null = null
    // is_unmounted はタイマー解除後に飛んできた応答を捨てるための印。
    let is_unmounted = false

    // ── Computed ──
    const focused_record = computed<SuggestionRecord | null>(() => records.value[focused_index.value] ?? null)

    const is_dark_theme = computed(() => theme.global.name.value === 'gkill_dark_theme')

    const has_records = computed(() => records.value.length > 0)

    // manual_tags は「手で足したタグ」。
    //
    // **これを出さないと、足したタグが画面のどこにも現れない。**
    // 候補のチップは focused_record.suggestions を描いているだけなので、
    // 候補に無いタグを選んでも表示される場所が無い。確定すれば実際には付くのだが、
    // 画面上は入力欄が空になるだけで、何も起きていないように見えてしまう。
    const manual_tags = computed<string[]>(() => {
        const suggested = new Set((focused_record.value?.suggestions ?? []).map((suggestion) => suggestion.tag))
        return [...selected_tags.value].filter((tag) => !suggested.has(tag))
    })

    // 次へ・前へを押せるか。スマートフォンではキーボードが使えないので、
    // j / k と同じ移動をボタンでも行えるようにしてある。
    const can_focus_prev = computed(() => focused_index.value > 0)
    const can_focus_next = computed(() => focused_index.value < records.value.length - 1)

    // ── Errors ──
    // report_error は失敗を画面に出す。
    // ログインが切れていた場合だけは、通知ではなくログイン画面へ戻す。
    function report_error(error: unknown): void {
        if (error instanceof UnauthorizedError) {
            on_unauthorized()
            return
        }
        push_message(String(error instanceof Error ? error.message : error), true)
    }

    async function sign_out(): Promise<void> {
        try {
            await logout()
        } catch {
            // 通信に失敗しても、手元では入っていない扱いにする。
        }
        on_unauthorized()
    }

    // ── Messages ──
    function push_message(text: string, is_error: boolean): void {
        const id = next_message_id++
        messages.value.push({ id, text, is_error })
        if (!is_error) {
            setTimeout(() => close_message(id), alert_auto_close_milli_seconds)
        }
    }

    function close_message(id: number): void {
        messages.value = messages.value.filter((message) => message.id !== id)
    }

    // ── Loading ──
    async function load(): Promise<void> {
        is_loading.value = true
        try {
            const response = await fetch_suggestions()
            records.value = response.records
            pending_count.value = response.pending
            skipped_count.value = response.skipped ?? 0
            focused_index.value = 0
            reset_selection()
        } catch (error) {
            report_error(error)
        } finally {
            is_loading.value = false
        }
    }

    // run_analyze は解析を始めて、終わるまで進み具合を見に行く。
    //
    // **解析の完了を1本の要求で待たない。** 写真の判定は1件で数分かかるので、
    // 待つ作りにするとタブを閉じただけで解析が止まる。
    // ここで待つのは画面の表示だけで、解析はサーバ側で走り続ける。
    async function run_analyze(): Promise<void> {
        if (is_analyzing.value) {
            return
        }
        is_analyzing.value = true
        analyze_done.value = 0
        analyze_total.value = 0
        try {
            apply_analyze_status(await start_analyze())
        } catch (error) {
            is_analyzing.value = false
            report_error(error)
            return
        }
        // 押した直後に終わっていることもある(判定対象が0件のときなど)。
        if (is_analyzing.value) {
            schedule_analyze_poll()
        }
    }

    function schedule_analyze_poll(): void {
        if (is_unmounted) {
            return
        }
        analyze_poll_timer_id = setTimeout(() => {
            void poll_analyze()
        }, analyze_poll_interval_milli_seconds)
    }

    async function poll_analyze(): Promise<void> {
        if (is_unmounted) {
            return
        }
        try {
            apply_analyze_status(await fetch_analyze_status())
        } catch (error) {
            is_analyzing.value = false
            report_error(error)
            return
        }
        if (is_analyzing.value) {
            schedule_analyze_poll()
        }
    }

    // apply_analyze_status は進み具合を画面へ反映し、終わっていれば知らせる。
    function apply_analyze_status(status: AnalyzeStatus): void {
        analyze_done.value = status.done
        analyze_total.value = status.total
        pending_count.value = status.pending

        if (status.running) {
            is_analyzing.value = true
            return
        }

        // ここから下は「走っていない」ときだけ。
        // 押した直後にまだ始まっていない場合と区別が付かないので、
        // 走らせていたときにだけ結果を出す。
        if (!is_analyzing.value) {
            return
        }
        is_analyzing.value = false

        if (status.failure) {
            push_message(`解析が途中で終わりました: ${status.failure}`, true)
            void load()
            return
        }
        if (status.report) {
            const report = status.report

            // 全件失敗は接続先か設定の問題。件数だけ出しても動けないので理由を出す。
            if (report.FailedRecords > 0 && report.FailedRecords === report.CandidateRecords) {
                push_message(
                    `判定する記録 ${report.CandidateRecords}件がすべて失敗しました: ${report.FailureReason}`,
                    true,
                )
                void load()
                return
            }

            let text = `解析しました: 提案あり ${report.SuggestedRecords}件 / 提案なし ${report.NoSuggestionRecords}件`
            if (report.FailedRecords > 0) {
                // 飛ばした記録があることは黙らない。次の解析でやり直される。
                text += ` / 判定できず ${report.FailedRecords}件 (${report.FailureReason})`
            }
            push_message(text, report.FailedRecords > 0)
        }
        void load()
    }

    // ── Selection ──
    function reset_selection(): void {
        selected_tags.value = new Set()
        manual_tag.value = ''
    }

    function toggle_tag(tag: string): void {
        // Set をそのまま書き換えても再描画されないので入れ替える。
        const next = new Set(selected_tags.value)
        if (next.has(tag)) {
            next.delete(tag)
        } else {
            next.add(tag)
        }
        selected_tags.value = next
    }

    function is_selected(tag: string): boolean {
        return selected_tags.value.has(tag)
    }

    function add_manual_tag(): void {
        const tag = manual_tag.value.trim()
        if (tag === '') {
            return
        }
        const next = new Set(selected_tags.value)
        next.add(tag)
        selected_tags.value = next
        manual_tag.value = ''
    }

    // ── Navigation ──
    function focus_next(): void {
        if (focused_index.value < records.value.length - 1) {
            focused_index.value++
            reset_selection()
        }
    }

    function focus_prev(): void {
        if (focused_index.value > 0) {
            focused_index.value--
            reset_selection()
        }
    }

    // ── Deciding ──
    // confirm は選んだタグだけを付ける。
    // 何も選ばずに確定した場合は「この記録にタグは要らない」という判定になり、
    // 次からは提案されない。記録の一定割合は元々そういうものなので、
    // これが最短の操作になるようにしてある。
    async function confirm(): Promise<void> {
        const record = focused_record.value
        if (!record || in_flight.has(record.target_id)) {
            return
        }

        const approve = [...selected_tags.value]
        in_flight.add(record.target_id)
        is_deciding.value = true

        // 押した瞬間に一覧から外す。失敗したら戻す。
        const removed_index = focused_index.value
        records.value = records.value.filter((item) => item.target_id !== record.target_id)
        if (focused_index.value > records.value.length - 1) {
            focused_index.value = Math.max(0, records.value.length - 1)
        }
        reset_selection()

        try {
            const response = await decide(record.target_id, approve)
            pending_count.value = response.pending
            if (approve.length === 0) {
                push_message('タグ不要として記録しました', false)
            } else {
                push_message(`${approve.join('、')} を付けました`, false)
            }

            // 一覧はサーバが返した先頭ぶんしか持っていない。
            // 見えている分を捌き切っても確認待ちが残っているなら続きを取りに行く。
            // これが無いと、まだ数千件あるのに「確認待ちの提案はありません」になる。
            if (records.value.length === 0 && response.pending > 0) {
                void load()
            }
        } catch (error) {
            records.value = [
                ...records.value.slice(0, removed_index),
                record,
                ...records.value.slice(removed_index),
            ]
            focused_index.value = removed_index
            report_error(error)
        } finally {
            in_flight.delete(record.target_id)
            is_deciding.value = false
        }
    }

    // reject は何も選ばずに確定するのと同じ。
    async function reject(): Promise<void> {
        reset_selection()
        await confirm()
    }

    // ── Theme ──
    function toggle_theme(): void {
        const next = is_dark_theme.value ? 'gkill_theme' : 'gkill_dark_theme'
        theme.global.name.value = next
        set_use_dark_theme(next === 'gkill_dark_theme')
    }

    // ── Keyboard ──
    // 文字を打っている最中や修飾キー付きでは反応しない。
    function is_typing(event: KeyboardEvent): boolean {
        const target = event.target as HTMLElement | null
        if (!target) {
            return false
        }
        const tag_name = target.tagName.toLowerCase()
        return tag_name === 'input' || tag_name === 'textarea' || target.isContentEditable
    }

    function on_keydown(event: KeyboardEvent): void {
        if (event.isComposing || event.repeat || event.ctrlKey || event.metaKey || event.altKey) {
            return
        }
        if (is_typing(event)) {
            return
        }
        if (!focused_record.value) {
            return
        }

        if (event.key >= '1' && event.key <= '9') {
            const index = Number(event.key) - 1
            const suggestion = focused_record.value.suggestions[index]
            if (suggestion) {
                event.preventDefault()
                toggle_tag(suggestion.tag)
            }
            return
        }

        switch (event.key) {
            case 'j':
            case 'ArrowDown':
                event.preventDefault()
                focus_next()
                break
            case 'k':
            case 'ArrowUp':
                event.preventDefault()
                focus_prev()
                break
            case 'Enter':
                event.preventDefault()
                void confirm()
                break
            case 'x':
            case 'Delete':
                event.preventDefault()
                void reject()
                break
            default:
                break
        }
    }

    // resume_analyze は、画面を開いた時点で解析が走っていれば表示を引き継ぐ。
    //
    // 解析はリクエストとは別に走っているので、再読込しても止まっていない。
    // 何も出さないと「押したのに何も起きていない」ように見える。
    async function resume_analyze(): Promise<void> {
        try {
            const status = await fetch_analyze_status()
            if (!status.running) {
                return
            }
            is_analyzing.value = true
            apply_analyze_status(status)
            schedule_analyze_poll()
        } catch {
            // 引き継ぎに失敗しても画面は使える。黙って諦める。
        }
    }

    // ── Lifecycle ──
    onMounted(() => {
        window.addEventListener('keydown', on_keydown)
        void load()
        void resume_analyze()
    })

    onUnmounted(() => {
        window.removeEventListener('keydown', on_keydown)
        // タイマーを解除しないと、画面を離れたあとも叩き続ける。
        is_unmounted = true
        if (analyze_poll_timer_id !== null) {
            clearTimeout(analyze_poll_timer_id)
            analyze_poll_timer_id = null
        }
    })

    // ── Return ──
    return {
        records,
        focused_index,
        focused_record,
        selected_tags,
        manual_tag,
        manual_tags,
        can_focus_prev,
        can_focus_next,
        is_loading,
        is_analyzing,
        is_deciding,
        is_dark_theme,
        has_records,
        pending_count,
        skipped_count,
        analyze_done,
        analyze_total,
        messages,
        load,
        run_analyze,
        toggle_tag,
        is_selected,
        add_manual_tag,
        focus_next,
        focus_prev,
        confirm,
        reject,
        toggle_theme,
        close_message,
        sign_out,
    }
}
