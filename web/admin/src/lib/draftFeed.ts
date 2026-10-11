import { ref } from 'vue'

/**
 * Bumped when revisions change (the server's signal) or this console
 * discards a draft, for the lists of drafts to read them again.
 */
export const draftsChanged = ref(0)
