import { computed, onMounted, onUnmounted, ref, type Ref } from 'vue'
import { useTheme } from 'vuetify'
import {
    analyze,
    decide,
    fetch_suggestions,
    logout,
    UnauthorizedError,
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
    const messages: Ref<AlertMessage[]> = ref([])

    let next_message_id = 0
    // in_flight は同じ記録への二重確定を防ぐ。
    const in_flight = new Set<string>()

    // ── Computed ──
    const focused_record = computed<SuggestionRecord | null>(() => records.value[focused_index.value] ?? null)

    const is_dark_theme = computed(() => theme.global.name.value === 'gkill_dark_theme')

    const has_records = computed(() => records.value.length > 0)

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
            focused_index.value = 0
            reset_selection()
        } catch (error) {
            report_error(error)
        } finally {
            is_loading.value = false
        }
    }

    async function run_analyze(): Promise<void> {
        is_analyzing.value = true
        try {
            const report = await analyze()
            push_message(
                `解析しました: 提案あり ${report.SuggestedRecords}件 / 提案なし ${report.NoSuggestionRecords}件`,
                false,
            )
            await load()
        } catch (error) {
            report_error(error)
        } finally {
            is_analyzing.value = false
        }
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

    // ── Lifecycle ──
    onMounted(() => {
        window.addEventListener('keydown', on_keydown)
        void load()
    })

    onUnmounted(() => {
        window.removeEventListener('keydown', on_keydown)
    })

    // ── Return ──
    return {
        records,
        focused_index,
        focused_record,
        selected_tags,
        manual_tag,
        is_loading,
        is_analyzing,
        is_deciding,
        is_dark_theme,
        has_records,
        pending_count,
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
