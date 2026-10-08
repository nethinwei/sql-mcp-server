<script setup lang="ts">
import {
  NConfigProvider, NDialogProvider, NGlobalStyle, NMessageProvider, NNotificationProvider, NLoadingBarProvider,
  type GlobalThemeOverrides,
} from 'naive-ui'
import { useEffectiveTheme } from '@/lib/theme'
import { naiveDateLocale, naiveLocale } from '@/i18n'

const { theme } = useEffectiveTheme()
const overrides: GlobalThemeOverrides = {
  common: { primaryColor: '#2f6fed', primaryColorHover: '#4b83f0', primaryColorPressed: '#2459c7', borderRadius: '6px' },
}
</script>

<template>
  <n-config-provider :theme="theme" :theme-overrides="overrides" :locale="naiveLocale" :date-locale="naiveDateLocale"
    style="height: 100%">
    <n-global-style />
    <n-loading-bar-provider>
      <n-dialog-provider>
        <n-notification-provider>
          <n-message-provider>
            <router-view />
          </n-message-provider>
        </n-notification-provider>
      </n-dialog-provider>
    </n-loading-bar-provider>
  </n-config-provider>
</template>

<style>
html, body, #app { height: 100%; margin: 0; }
body { font-family: -apple-system, BlinkMacSystemFont, 'PingFang SC', 'Microsoft YaHei', sans-serif; }
.alert-actions { margin-top: 10px; display: flex; gap: 8px; }
.dialog { width: 480px; max-width: calc(100vw - 32px); }
code, .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12.5px; }
</style>
