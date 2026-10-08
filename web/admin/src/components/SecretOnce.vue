<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NInputGroup, NInput, useMessage } from 'naive-ui'

// Shows a secret that cannot be retrieved again, with a copy button.
defineProps<{ secret: string; note?: string }>()
const { t } = useI18n()
const message = useMessage()

async function copy(secret: string) {
  await navigator.clipboard.writeText(secret)
  message.success(t('common.copied'))
}
</script>

<template>
  <n-alert type="warning" :show-icon="false">
    <div class="note">{{ note ?? t('secret.once') }}</div>
    <n-input-group>
      <n-input :value="secret" readonly class="mono" />
      <n-button type="primary" @click="copy(secret)">{{ t('common.copy') }}</n-button>
    </n-input-group>
  </n-alert>
</template>

<style scoped>
.note { margin-bottom: 8px; }
</style>
