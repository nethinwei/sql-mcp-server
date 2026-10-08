import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { run } from '@/api/client'
import { ConfigSchemaQuery } from '@/api/ops'
import { at, localize, type Path, type Schema } from './jsonSchema'

// The configuration JSON Schema, generated from the Go configuration structs
// and served by the API: the console takes field rules, help, defaults and
// option lists from it instead of keeping its own copies.
const raw = ref<Schema | null>(null)
let loading: Promise<void> | null = null

function load() {
  loading ??= run(ConfigSchemaQuery, {})
    .then((d) => { raw.value = d.configSchema as Schema })
    .catch(() => { loading = null })
  return loading
}

/** The schema in the console language; loads it on first use. */
export function useConfigSchema() {
  const { locale } = useI18n()
  void load()
  const schema = computed(() => (raw.value ? localize(raw.value, locale.value) : null))
  /** Allowed values, or suggestions where any value is accepted. */
  const options = (path: Path): string[] => {
    const s = schema.value ? at(schema.value, schema.value, path) : undefined
    return ((s?.enum ?? s?.examples ?? []) as unknown[]).map(String)
  }
  return { schema, options }
}
