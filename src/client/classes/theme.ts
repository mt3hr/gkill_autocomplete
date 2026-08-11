// テーマの記憶。gkill 本体と同じくクッキーに置く。
//
// サーバへ問い合わせる前に決まるので、表示が一瞬切り替わるのを避けられる。
const use_dark_theme_cookie_key = 'use_dark_theme'
const cookie_max_age_seconds = 86400 * 400

export function get_use_dark_theme(): boolean {
    const cookies = document.cookie.split(';')
    const found = cookies
        .find((cookie) => cookie.split('=')[0].trim() === use_dark_theme_cookie_key)
        ?.replace(`${use_dark_theme_cookie_key}=`, '')
        .trim()

    if (found === undefined) {
        return false
    }
    try {
        return JSON.parse(found) as boolean
    } catch {
        // 壊れた値が入っていたらライトに倒す。
        return false
    }
}

export function set_use_dark_theme(use_dark_theme: boolean): void {
    document.cookie = `${use_dark_theme_cookie_key}=${use_dark_theme}; max-age=${cookie_max_age_seconds}; path=/`
}
