<script setup lang="ts">
import { ref } from 'vue'
import { login } from '@/classes/api'

const emits = defineEmits<{ (event: 'logged_in'): void }>()

const user_id = ref('')
const password = ref('')
const error_message = ref('')
const is_sending = ref(false)

async function submit(): Promise<void> {
    if (is_sending.value) {
        return
    }
    error_message.value = ''
    is_sending.value = true
    try {
        await login(user_id.value, password.value)
        password.value = ''
        emits('logged_in')
    } catch (error) {
        error_message.value = String(error instanceof Error ? error.message : error)
    } finally {
        is_sending.value = false
    }
}
</script>

<template>
    <v-main class="login_main">
        <v-container class="login_container">
            <v-card class="login_card">
                <v-card-title>タグ提案</v-card-title>
                <v-card-subtitle>gkill のアカウントで入ってください</v-card-subtitle>

                <v-card-text>
                    <v-form @submit.prevent="submit()">
                        <v-text-field v-model="user_id" label="利用者ID" autofocus autocomplete="username"
                            :disabled="is_sending" />
                        <v-text-field v-model="password" label="パスワード" type="password"
                            autocomplete="current-password" :disabled="is_sending"
                            @keydown.enter.prevent="submit()" />

                        <v-alert v-if="error_message" color="error" role="alert" class="mt-2" density="compact">
                            {{ error_message }}
                        </v-alert>

                        <v-btn type="submit" color="primary" variant="flat" block class="mt-4"
                            :loading="is_sending">
                            入る
                        </v-btn>
                    </v-form>
                </v-card-text>

                <v-card-text class="text-caption text-medium-emphasis">
                    この画面には未確認の提案が並びます。記録の本文と写真がそのまま出るので、
                    人に見られる場所では開かないでください。
                </v-card-text>
            </v-card>
        </v-container>
    </v-main>
</template>

<style scoped>
.login_main {
    padding-top: 0 !important;
}

.login_container {
    max-width: 420px;
    padding-top: 12vh;
}

.login_card {
    overflow: hidden;
}
</style>
