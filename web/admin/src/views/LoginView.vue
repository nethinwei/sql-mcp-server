<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCard, NForm, NFormItem, NInput, NSpace } from 'naive-ui'
import { login } from '@/api/session'
import { useWorkspace } from '@/stores/workspace'
import ThemeSwitch from '@/components/ThemeSwitch.vue'
import LocaleSwitch from '@/components/LocaleSwitch.vue'
import logo from '@/assets/logo.svg'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const ws = useWorkspace()
const username = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  busy.value = true
  error.value = ''
  try {
    const err = await login(username.value.trim(), password.value)
    if (err) {
      error.value = err || t('login.failed')
      return
    }
    if (!ws.baseId) await ws.loadPublished().catch(() => false)
    await router.replace(typeof route.query.next === 'string' ? route.query.next : '/')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="wrap">
    <n-space class="corner" :size="4"><locale-switch /><theme-switch /></n-space>
    <n-card class="card" :bordered="false">
      <div class="title"><img :src="logo" class="logo" alt="" /> {{ t('app.title') }}</div>
      <p class="sub">{{ t('app.subtitle') }}</p>
      <n-form @submit.prevent="submit">
        <n-form-item :label="t('login.username')" :show-feedback="false" class="field">
          <n-input v-model:value="username" autofocus :input-props="{ autocomplete: 'username' }" />
        </n-form-item>
        <n-form-item :label="t('login.password')" :show-feedback="false" class="field last">
          <n-input v-model:value="password" type="password" show-password-on="click"
            :input-props="{ autocomplete: 'current-password' }" @keyup.enter="submit" />
        </n-form-item>
        <n-alert v-if="error" type="error" :show-icon="false" class="error">{{ error }}</n-alert>
        <n-button type="primary" block :loading="busy" :disabled="!username || !password" attr-type="submit">
          {{ t('login.submit') }}
        </n-button>
      </n-form>
    </n-card>
  </div>
</template>

<style scoped>
.wrap { min-height: 100%; display: flex; align-items: center; justify-content: center; padding: 24px; box-sizing: border-box;
  background: linear-gradient(160deg, rgba(47,111,237,.10), transparent 60%); }
.corner { position: fixed; top: 14px; right: 16px; }
.card { width: 360px; max-width: 100%; box-shadow: 0 8px 32px rgba(0,0,0,.12); }
.title { font-size: 20px; font-weight: 600; display: flex; align-items: center; gap: 8px; }
.logo { width: 32px; height: 32px; flex: none; }
.sub { margin: 6px 0 22px; opacity: .6; font-size: 13px; }
.field { margin-bottom: 14px; }
.field.last { margin-bottom: 18px; }
.error { margin-bottom: 14px; }
</style>
