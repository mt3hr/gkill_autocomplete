<script setup lang="ts">
import { useTagSuggestionPage } from '@/classes/use-tag-suggestion-page'
import AttachedTag from './views/attached-tag.vue'

const props = defineProps<{
    // user_id はログインした利用者。誰として見ているかを画面に出す。
    user_id: string
    // is_analyzable が偽なら、このアカウントは起動時に --user へ
    // 渡されていない。ログインはできるが提案は無い。
    is_analyzable: boolean
}>()

const emits = defineEmits<{ (event: 'logged_out'): void }>()

const {
    records,
    focused_index,
    focused_record,
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
    analyze_done,
    analyze_total,
    messages,
    load,
    run_analyze,
    toggle_tag,
    is_selected,
    add_manual_tag,
    focus_prev,
    focus_next,
    confirm,
    reject,
    toggle_theme,
    close_message,
    sign_out,
} = useTagSuggestionPage(() => emits('logged_out'))

function format_time(value: string): string {
    const parsed = new Date(value)
    if (Number.isNaN(parsed.getTime())) {
        return value
    }
    return parsed.toLocaleString()
}

function format_confidence(value: number): string {
    return `${Math.round(value * 100)}%`
}
</script>

<template>
    <v-app-bar :height="50" color="primary" app flat>
        <v-toolbar-title>タグ提案</v-toolbar-title>
        <v-spacer />
        <span v-if="props.user_id" class="signed_in_user mr-3">{{ props.user_id }}</span>
        <span class="pending_count mr-2">確認待ち {{ pending_count }}件</span>
        <!-- 解析は数十分かかることがある。走っている間は何件目かを出す。 -->
        <span v-if="is_analyzing" class="analyze_progress mr-2">
            解析中 {{ analyze_total > 0 ? `${analyze_done} / ${analyze_total}` : '準備中' }}
        </span>
        <v-tooltip :text="is_analyzing ? '解析しています' : '解析する'">
            <template v-slot:activator="{ props: tooltip_props }">
                <v-btn v-bind="tooltip_props" icon="mdi-refresh" :loading="is_analyzing"
                    :disabled="!props.is_analyzable" @click="run_analyze()" />
            </template>
        </v-tooltip>
        <v-tooltip text="一覧を読み直す">
            <template v-slot:activator="{ props }">
                <v-btn v-bind="props" icon="mdi-reload" :loading="is_loading" @click="load()" />
            </template>
        </v-tooltip>
        <v-tooltip :text="is_dark_theme ? 'ライトテーマにする' : 'ダークテーマにする'">
            <template v-slot:activator="{ props }">
                <v-btn v-bind="props" :icon="is_dark_theme ? 'mdi-weather-sunny' : 'mdi-weather-night'"
                    @click="toggle_theme()" />
            </template>
        </v-tooltip>
        <v-tooltip text="ログアウト">
            <template v-slot:activator="{ props }">
                <v-btn v-bind="props" icon="mdi-logout" @click="sign_out()" />
            </template>
        </v-tooltip>
    </v-app-bar>

    <v-main class="main">
        <div class="alert_container">
            <v-slide-y-transition group>
                <v-alert v-for="message in messages" :key="message.id" :color="message.is_error ? 'error' : undefined"
                    :role="message.is_error ? 'alert' : undefined" closable @click:close="close_message(message.id)">
                    {{ message.text }}
                </v-alert>
            </v-slide-y-transition>
        </div>

        <v-container class="page_container">
            <div v-if="is_loading" class="empty_state">
                <v-progress-circular indeterminate color="primary" />
            </div>

            <div v-else-if="!props.is_analyzable" class="empty_state">
                <v-icon size="48" color="primary">mdi-account-off-outline</v-icon>
                <p class="mt-4">このアカウントは解析の対象に含まれていません。</p>
                <p class="text-medium-emphasis">
                    対象にするには、起動時に <code>--user {{ props.user_id }}</code> を渡してください。
                </p>
            </div>

            <div v-else-if="!has_records" class="empty_state">
                <v-icon size="48" color="primary">mdi-check-circle-outline</v-icon>
                <p class="mt-4">確認待ちの提案はありません。</p>
                <p class="text-medium-emphasis">
                    右上の更新ボタンで解析すると、まだタグの付いていない記録を調べます。
                </p>
            </div>

            <template v-else-if="focused_record">
                <!-- スマートフォンではキーボードが使えないので、j / k と同じ移動をボタンでも出す。
                     確定・タグ不要とは離しておく。並べると移動のつもりで判定を押してしまう。 -->
                <div class="nav_row">
                    <v-btn variant="tonal" size="small" :disabled="!can_focus_prev" @click="focus_prev()">
                        <v-icon start>mdi-chevron-left</v-icon>前 (k)
                    </v-btn>
                    <div class="progress_line text-medium-emphasis">
                        {{ focused_index + 1 }} / {{ records.length }}
                    </div>
                    <v-btn variant="tonal" size="small" :disabled="!can_focus_next" @click="focus_next()">
                        次 (j)<v-icon end>mdi-chevron-right</v-icon>
                    </v-btn>
                </div>

                <v-card class="record_card">
                    <div v-if="focused_record.is_image && focused_record.thumb_url" class="record_image_wrap">
                        <!-- 画像は自分のサーバが gkill から取り寄せて中継する。
                             ディスクには残さない。 -->
                        <img :src="focused_record.thumb_url" class="record_image" alt="記録の写真" />
                    </div>

                    <!-- 画像でないファイルの記録。写真も本文も持たないので、
                         出さないと日時と種別しか載らない空の札になる。
                         gkill は画像以外のサムネイルを作らないため、
                         ここでプレビューを出すことはできない。 -->
                    <div v-else-if="focused_record.file_name" class="record_file_wrap">
                        <v-icon size="32" color="primary">mdi-file-outline</v-icon>
                        <span class="record_file_name">{{ focused_record.file_name }}</span>
                    </div>

                    <v-card-text>
                        <div class="text-medium-emphasis text-caption mb-1">
                            {{ format_time(focused_record.related_time) }} ・ {{ focused_record.data_type }}
                        </div>
                        <pre v-if="focused_record.text" class="record_text">{{ focused_record.text }}</pre>
                        <div class="mt-2 existing_tags">
                            <span class="text-caption text-medium-emphasis mr-1">付いているタグ:</span>
                            <template v-if="focused_record.existing_tags && focused_record.existing_tags.length > 0">
                                <AttachedTag v-for="tag in focused_record.existing_tags" :key="tag" :tag="tag" />
                            </template>
                            <span v-else class="text-caption text-medium-emphasis">なし</span>
                        </div>
                    </v-card-text>

                    <v-divider />

                    <v-card-text>
                        <div class="text-caption text-medium-emphasis mb-2">
                            付けるタグを選んでください。複数選べます。
                            <strong>何も選ばずに確定すると「タグ不要」として記録します。</strong>
                        </div>

                        <div class="candidate_list">
                            <v-chip v-for="(suggestion, index) in focused_record.suggestions" :key="suggestion.id"
                                :color="is_selected(suggestion.tag) ? 'primary' : undefined"
                                :variant="is_selected(suggestion.tag) ? 'flat' : 'outlined'" class="mr-2 mb-2"
                                @click="toggle_tag(suggestion.tag)">
                                <span class="candidate_key">{{ index + 1 }}</span>
                                {{ suggestion.tag }}
                                <span class="candidate_confidence">{{ format_confidence(suggestion.confidence) }}</span>
                            </v-chip>
                        </div>

                        <!-- 手で足したタグ。上の候補チップは suggestions しか描かないので、
                             ここが無いと足したタグが画面のどこにも現れない。 -->
                        <div v-if="manual_tags.length > 0" class="candidate_list">
                            <v-chip v-for="tag in manual_tags" :key="'manual-' + tag" color="primary" variant="flat"
                                closable class="mr-2 mb-2" @click:close="toggle_tag(tag)">
                                <v-icon start size="small">mdi-tag-plus-outline</v-icon>
                                {{ tag }}
                            </v-chip>
                        </div>

                        <div class="reason_list">
                            <div v-for="suggestion in focused_record.suggestions" :key="suggestion.id"
                                class="text-caption text-medium-emphasis">
                                {{ suggestion.tag }}: {{ suggestion.reason }}（{{ suggestion.tier }}）
                            </div>
                        </div>

                        <div class="manual_tag_row mt-3">
                            <v-text-field v-model="manual_tag" label="自分でタグを足す" density="compact" hide-details
                                @keydown.enter.prevent="add_manual_tag()" />
                            <v-btn class="ml-2" variant="tonal" @click="add_manual_tag()">追加</v-btn>
                        </div>
                    </v-card-text>

                    <v-card-actions class="gkill-dialog-actions">
                        <v-btn variant="tonal" :disabled="is_deciding" @click="reject()">
                            タグ不要 (x)
                        </v-btn>
                        <v-spacer />
                        <v-btn color="primary" variant="flat" :loading="is_deciding" @click="confirm()">
                            確定 (Enter)
                        </v-btn>
                    </v-card-actions>
                </v-card>

                <div class="shortcut_help text-caption text-medium-emphasis mt-3">
                    j / k で前後へ ・ 1〜9 で候補を選ぶ ・ Enter で確定 ・ x でタグ不要
                </div>
            </template>
        </v-container>
    </v-main>
</template>

<style scoped>
.main {
    padding-top: 50px !important;
}

.page_container {
    max-width: 720px;
}

.empty_state {
    text-align: center;
    padding: 64px 16px;
}

.nav_row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    margin-bottom: 4px;
}

.progress_line {
    text-align: center;
    flex: 1 1 auto;
}

.record_card {
    overflow: hidden;
}

.record_image_wrap {
    display: flex;
    justify-content: center;
    background-color: rgb(var(--v-theme-background-focused));
}

.record_image {
    max-width: 100%;
    max-height: 45vh;
    object-fit: contain;
}

/* 画像でないファイルの記録。プレビューの代わりに名前を出す。 */
.record_file_wrap {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 16px;
    background-color: rgb(var(--v-theme-background-focused));
}

.record_file_name {
    word-break: break-all;
}

.record_text {
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 20vh;
    overflow-y: auto;
    font-family: inherit;
    margin: 0;
}

.candidate_key {
    display: inline-block;
    min-width: 1.2em;
    margin-right: 6px;
    opacity: 0.7;
}

.candidate_confidence {
    margin-left: 8px;
    opacity: 0.7;
    font-size: 0.85em;
}

.reason_list {
    margin-top: 4px;
}

/* 付いているタグの行。タグ同士が詰まりすぎないよう少し空ける。 */
.existing_tags {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 2px;
}

.manual_tag_row {
    display: flex;
    align-items: center;
}

.pending_count {
    font-size: 0.9rem;
}

/* 解析の進み具合。件数だけを出す(記録の中身は出さない)。 */
.analyze_progress {
    font-size: 0.9rem;
    white-space: nowrap;
}

/* 誰として見ているか。複数のアカウントを行き来するときの取り違え防止。 */
.signed_in_user {
    font-size: 0.9rem;
    opacity: 0.85;
}

.shortcut_help {
    text-align: center;
}
</style>
