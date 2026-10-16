<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { NButton } from 'naive-ui'

// Expands or collapses a list that shows only its first `cap` items.
defineProps<{ count: number; cap: number }>()
const expanded = defineModel<boolean>()
const { t } = useI18n()
</script>

<template>
  <n-button v-if="count > cap" text type="primary" size="tiny" @click="expanded = !expanded">
    {{ expanded ? t('common.showLess') : t('common.showMore', { n: count - cap }) }}
  </n-button>
</template>
