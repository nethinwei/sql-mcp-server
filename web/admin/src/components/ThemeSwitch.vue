<script setup lang="ts">
import { computed, h } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NDropdown, NIcon, NTooltip } from 'naive-ui'
import { ContrastOutline, MoonOutline, SunnyOutline } from '@vicons/ionicons5'
import { themeMode, themeModes, type ThemeMode } from '@/lib/theme'

const { t } = useI18n()
const icons = { auto: ContrastOutline, light: SunnyOutline, dark: MoonOutline }
const options = computed(() => themeModes.map((m) => ({
  key: m.value,
  label: t(m.labelKey),
  icon: () => h(NIcon, null, { default: () => h(icons[m.value]) }),
  props: { style: m.value === themeMode.value ? 'font-weight: 600' : '' },
})))
const current = computed(() => t(themeModes.find((m) => m.value === themeMode.value)!.labelKey))
</script>

<template>
  <n-dropdown :options="options" trigger="click" @select="(k: ThemeMode) => (themeMode = k)">
    <span>
      <n-tooltip>
        <template #trigger>
          <n-button quaternary circle size="small" :aria-label="t('prefs.theme')">
            <template #icon><n-icon><component :is="icons[themeMode]" /></n-icon></template>
          </n-button>
        </template>
        {{ t('prefs.theme') }}: {{ current }}
      </n-tooltip>
    </span>
  </n-dropdown>
</template>
