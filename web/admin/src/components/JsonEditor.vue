<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useThemeVars } from 'naive-ui'
import { Compartment, EditorState } from '@codemirror/state'
import {
  EditorView, drawSelection, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers,
} from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { bracketMatching, foldGutter, foldKeymap, indentOnInput } from '@codemirror/language'
import { json } from '@codemirror/lang-json'
import { autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap } from '@codemirror/autocomplete'
import { lintGutter, lintKeymap } from '@codemirror/lint'
import { look, schemaCompletions, schemaSupport, type SchemaSource } from '@/lib/jsonEditor'
import { useEffectiveTheme } from '@/lib/theme'
import type { Path, Schema } from '@/lib/jsonSchema'

// A JSON editor with highlighting, schema completion (Ctrl/Cmd+Space or as
// you type), hover help on property names and inline checks.
const props = defineProps<{ modelValue: string; schema: Schema | null; path: Path; readonly?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const { t } = useI18n()
const vars = useThemeVars()
const { dark: isDark } = useEffectiveTheme()
const host = ref<HTMLElement>()
let view: EditorView | undefined

const source = (): SchemaSource => ({ root: props.schema, path: props.path })
const translate = (key: string, params?: Record<string, unknown>) => t(key, params ?? {})
const lookC = new Compartment()
const editableC = new Compartment()

const palette = computed(() => {
  const dark = isDark.value
  return {
    dark,
    text: vars.value.textColor2,
    muted: vars.value.textColor3,
    background: vars.value.inputColor,
    border: vars.value.borderColor,
    primary: vars.value.primaryColor,
    popover: vars.value.popoverColor,
    selection: dark ? 'rgba(99,142,255,.28)' : 'rgba(47,111,237,.16)',
    key: dark ? '#7cacf8' : '#1f5bd6',
    string: dark ? '#7ec699' : '#1a7f37',
    number: dark ? '#f0a35e' : '#b35900',
    literal: dark ? '#c792ea' : '#8250df',
  }
})
const editable = () => [EditorState.readOnly.of(Boolean(props.readonly)), EditorView.editable.of(!props.readonly)]

onMounted(() => {
  view = new EditorView({
    parent: host.value,
    state: EditorState.create({
      doc: props.modelValue,
      extensions: [
        lineNumbers(), highlightActiveLineGutter(), foldGutter(), drawSelection(), history(), indentOnInput(),
        bracketMatching(), closeBrackets(), highlightActiveLine(), lintGutter(), json(),
        autocompletion({ override: [schemaCompletions(source, translate)], icons: false }),
        schemaSupport(source, translate),
        keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...historyKeymap, ...foldKeymap, ...completionKeymap,
          ...lintKeymap, indentWithTab]),
        EditorState.tabSize.of(2),
        lookC.of(look(palette.value)),
        editableC.of(editable()),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) emit('update:modelValue', u.state.doc.toString())
        }),
      ],
    }),
  })
})
onBeforeUnmount(() => view?.destroy())

// Outside changes (restore, format) replace the document.
watch(() => props.modelValue, (v) => {
  if (view && v !== view.state.doc.toString()) {
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: v } })
  }
})
watch(palette, (p) => view?.dispatch({ effects: lookC.reconfigure(look(p)) }))
watch(() => props.readonly, () => view?.dispatch({ effects: editableC.reconfigure(editable()) }))
</script>

<template>
  <div ref="host" class="json-editor" />
</template>

<style scoped>
.json-editor :deep(.cm-editor) { max-height: 520px; }
.json-editor :deep(.cm-scroller) { overflow: auto; }
</style>
