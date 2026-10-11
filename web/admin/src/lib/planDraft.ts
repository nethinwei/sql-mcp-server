import { ref } from 'vue'
import type { Plan } from './plan'

/** A plan handed to the plan page, for example from a selection of tables. */
export const planDraft = ref<Plan | null>(null)
