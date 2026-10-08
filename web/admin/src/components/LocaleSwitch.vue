<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NDropdown, NIcon, NTooltip } from 'naive-ui'
import { LanguageOutline } from '@vicons/ionicons5'
import { locale, locales, type Locale } from '@/i18n'

const { t } = useI18n()
const options = computed(() => locales.map((l) => ({
  key: l.value,
  label: l.label,
  props: { style: l.value === locale.value ? 'font-weight: 600' : '' },
})))
</script>

<template>
  <n-dropdown :options="options" trigger="click" @select="(k: Locale) => (locale = k)">
    <span>
      <n-tooltip>
        <template #trigger>
          <n-button quaternary circle size="small" :aria-label="t('prefs.language')">
            <template #icon><n-icon><language-outline /></n-icon></template>
          </n-button>
        </template>
        {{ t('prefs.language') }}: {{ locales.find((l) => l.value === locale)?.label }}
      </n-tooltip>
    </span>
  </n-dropdown>
</template>
