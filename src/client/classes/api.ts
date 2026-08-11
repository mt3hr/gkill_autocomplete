// 確認画面から自分のサーバへ問い合わせる口。
//
// 相手は同じ端末で動く gkill_autocomplete 自身なので、
// 認証も接続先の設定も要らない。

export type Suggestion = {
    id: string
    tag: string
    confidence: number
    tier: string
    reason: string
}

export type SuggestionRecord = {
    target_id: string
    data_type: string
    related_time: string
    is_image: boolean
    thumb_url: string
    text: string
    existing_tags: string[] | null
    suggestions: Suggestion[]
}

export type SuggestionsResponse = {
    records: SuggestionRecord[]
    pending: number
}

export type DecideResponse = {
    approved: number
    rejected: number
    pending: number
}

export type AnalyzeResponse = {
    LearnedRecords: number
    CandidateRecords: number
    SuggestedRecords: number
    NoSuggestionRecords: number
    StoredSuggestions: number
    SkippedByVerdict: number
    pending: number
}

// UnauthorizedError はログインしていない、または期限が切れたことを表す。
//
// 画面はこれを見てログインへ戻す。
export class UnauthorizedError extends Error {
    constructor(message: string) {
        super(message)
        this.name = 'UnauthorizedError'
    }
}

async function post<T>(path: string, body: unknown): Promise<T> {
    const response = await fetch(path, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    })

    const parsed = (await response.json()) as T & { error?: string }

    if (response.status === 401) {
        throw new UnauthorizedError(parsed.error ?? 'ログインしてください')
    }
    if (!response.ok || parsed.error) {
        throw new Error(parsed.error ?? `サーバが HTTP ${response.status} を返しました`)
    }
    return parsed
}

export type SessionState = {
    authenticated: boolean
    user_id: string
    // analyzable が偽なら、ログインはできても提案が無い。
    // 起動時に --user へ渡されなかったアカウントがこれに当たる。
    analyzable: boolean
}

export async function fetch_session(): Promise<SessionState> {
    const response = await fetch('/api/session')
    if (!response.ok) {
        return { authenticated: false, user_id: '', analyzable: false }
    }
    const parsed = (await response.json()) as Partial<SessionState>
    return {
        authenticated: parsed.authenticated === true,
        user_id: parsed.user_id ?? '',
        analyzable: parsed.analyzable === true,
    }
}

// password_sha256 は平文パスワードの SHA-256 を小文字16進64桁にする。
//
// gkill の画面と同じ形。**平文をブラウザから出さない**ためのもので、
// gkill 側もこの値を資格情報として受け取る(保存時にだけ Argon2id を掛ける)。
//
// crypto.subtle は安全なコンテキスト(https:// か localhost)でしか使えない。
// 使えない場合は、平文を送るのではなく理由を出して止める。
async function password_sha256(password: string): Promise<string> {
    if (!globalThis.crypto?.subtle) {
        throw new Error(
            'この接続ではパスワードを安全に扱えません。' +
            'https:// で開くか、同じ端末から localhost で開いてください。'
        )
    }
    const encoded = new TextEncoder().encode(password)
    const digest = await globalThis.crypto.subtle.digest('SHA-256', encoded)
    return Array.from(new Uint8Array(digest))
        .map((byte) => byte.toString(16).padStart(2, '0'))
        .join('')
}

// login は gkill のアカウントで確認画面へ入る。
//
// 照合は gkill のアカウントDBに対して行われる。gkill の /api/login は
// 叩かないので、ここで何度失敗しても gkill 側のログイン回数は減らない。
export async function login(user_id: string, password: string): Promise<{ ok: boolean }> {
    return post<{ ok: boolean }>('/api/login', {
        user_id,
        password_sha256: await password_sha256(password),
    })
}

export function logout(): Promise<{ ok: boolean }> {
    return post<{ ok: boolean }>('/api/logout', {})
}

export function fetch_suggestions(): Promise<SuggestionsResponse> {
    return post<SuggestionsResponse>('/api/suggestions', {})
}

// decide は承認するタグを送る。
//
// 承認しなかった提案は、同じ記録の分がまとめて却下になる。
// 1つも承認しない場合は「この記録にタグは要らない」という判定になり、
// 次からは提案されなくなる。
export function decide(target_id: string, approve_tags: string[]): Promise<DecideResponse> {
    return post<DecideResponse>('/api/decide', {
        target_id,
        approve_tags,
        no_tag_needed: approve_tags.length === 0,
    })
}

export function analyze(): Promise<AnalyzeResponse> {
    return post<AnalyzeResponse>('/api/analyze', {})
}
