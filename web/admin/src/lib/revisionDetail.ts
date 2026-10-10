import { computed, watch, type Ref } from 'vue'
import { run } from '@/api/client'
import { DiffQuery, RevisionYamlQuery } from '@/api/ops'
import { createResource } from '@/lib/asyncResource'

/**
 * The detail of revision id: its YAML, and its diff from revision from (none
 * when from is unset or the revision itself). Each loads again when what it
 * depends on changes (what a draft builds on may arrive after the revision),
 * and only the latest load counts, so a late answer never shows under a
 * newer selection or base.
 */
export function useRevisionDetail(id: Ref<string | null>, from: Ref<string | null>) {
  const yaml = createResource(async (rev: string) => (await run(RevisionYamlQuery, { id: rev })).revision?.yaml ?? '')
  const diff = createResource(async (base: string | null, rev: string) =>
    base && base !== rev ? (await run(DiffQuery, { from: base, to: rev })).diff : '')
  watch(id, (rev) => (rev ? void yaml.load(rev) : yaml.reset()), { immediate: true })
  watch([from, id], ([base, rev]) => (rev ? void diff.load(base, rev) : diff.reset()), { immediate: true })
  return {
    yaml: computed(() => yaml.state.data ?? ''),
    diff: computed(() => diff.state.data ?? ''),
    loading: computed(() => yaml.state.status === 'loading' || diff.state.status === 'loading'),
    error: computed(() => diff.state.error || yaml.state.error),
  }
}
