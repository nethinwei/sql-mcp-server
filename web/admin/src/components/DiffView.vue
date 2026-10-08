<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NEmpty } from 'naive-ui'

// Renders a unified diff with added/removed lines highlighted.
const props = defineProps<{ diff: string }>()
const { t } = useI18n()

const lines = computed(() =>
  props.diff.split('\n').filter((l, i, all) => !(i === all.length - 1 && l === '')).map((text) => ({
    text,
    kind: text.startsWith('@@') ? 'hunk'
      : text.startsWith('+++') || text.startsWith('---') ? 'file'
        : text.startsWith('+') ? 'add'
          : text.startsWith('-') ? 'del' : 'ctx',
  })),
)
</script>

<template>
  <n-empty v-if="!diff.trim()" :description="t('diff.none')" />
  <pre v-else class="diff mono"><div v-for="(l, i) in lines" :key="i" :class="l.kind">{{ l.text || ' ' }}</div></pre>
</template>

<style scoped>
.diff { margin: 0; padding: 10px 0; border: 1px solid rgba(128,128,128,.25); border-radius: 6px; overflow: auto; max-height: 560px; line-height: 1.55; }
.diff div { padding: 0 14px; white-space: pre; min-width: fit-content; }
.add { background: rgba(46, 160, 67, .18); }
.del { background: rgba(248, 81, 73, .18); }
.hunk { color: #8250df; background: rgba(130, 80, 223, .10); }
.file { opacity: .6; }
</style>
