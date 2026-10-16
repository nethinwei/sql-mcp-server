import { defineStore } from 'pinia'
import { computed, isReactive, markRaw, toRaw, type ComputedRef } from 'vue'
// The server's merge rules, shared on purpose: the Go fixture is the one
// source, locked by TestMergeSchemaFixture (regenerate it with
// `go test ./x/admin/configapp -run TestMergeSchemaFixture -update`).
import mergeSchema from '../../../../x/admin/configapp/testdata/merge_schema.json'
import { EntityIndex, entityId } from '@/lib/entityRefs'
import type {
  Action,
  WorkspaceQuery as WorkspaceResult,
  EntityPartsFragment,
  PreviewPlanQuery,
  WorkspacePartsFragment,
} from '@/gql/graphql'
import type { FieldInput, EntityInput, GrantInput, RoleInput, UserInput } from '@/gql/schema'

export interface Datasource {
  name: string
  driver: string
  dsn: string
  connections: { name: string; dsn: string; role: string; pooler?: string | null }[]
  routing: { read: string; write: string; execute: string }
  readAfterWrite?: string | null
  /** Set when the datasource admits execute_sql (the SQL escape hatch). */
  sqlEscape?: { connection: string } | null
}

/** What the workspace edits, as the server keeps it (see x/admin/configapp). */
export interface Sections {
  entities: EntityInput[]
  roles: RoleInput[]
  users: UserInput[]
  settings: Record<string, unknown>
}

/** A plan's outcome as previewPlan answers it. */
export type Proposal = PreviewPlanQuery['previewPlan']

export interface Change {
  kind: 'entity' | 'role' | 'user' | 'settings'
  /** The role or user name, or the entity ID (see entityId). */
  name: string
  type: 'added' | 'removed' | 'modified'
  /** For a settings conflict, the field paths changed on both sides. */
  paths?: string[]
}

/** A conflict of the workspace, as the server holds it (see core/workspace). */
export type Conflict = WorkspacePartsFragment['conflicts'][number]

/** Where changes came from: a plan an agent applied, a draft taken in. */
export type Note = WorkspacePartsFragment['notes'][number]

interface State extends Sections {
  /** The administrator this workspace is; '' until bound (see bindTo). */
  owner: string
  baseId: string | null
  /** The published revision when the workspace was loaded or rebased. */
  basePublished: string | null
  baseHash: string
  datasources: Datasource[]
  /** Users that had a token in the base revision. */
  /** Each user's token hash in the base revision ('' or absent: no token). */
  baseTokens: Record<string, string>
  baseline: string
  /** The workspace version the server last answered with. */
  version: number
  /** The workspace as the server last answered for it (see Copy); null: not read yet. */
  copy: Copy | null
  /** The server's: conflicts to resolve, notes, drafts taken or saved, the draft it holds. */
  conflicts: Conflict[]
  notes: Note[]
  taken: string[]
  checkpoint: string | null
  loading: boolean
  /**
   * Items renamed or moved here and not yet written, by itemRef: the key the
   * server knows them by. They are written with renamedFrom, so the server
   * moves the references to them (see x/admin/configapp reconcile).
   */
  renamed: Record<string, string>
}

/**
 * The items of the workspace kept on the server as this console last read
 * or wrote them, by itemRef: an item edited here is what differs from it,
 * written with the value it was edited from (raw), so the server merges
 * what was written there meanwhile.
 */
export interface Copy {
  values: Record<string, string>
  raw: Record<string, unknown>
}

/** Sets ref's value in copy; undefined removes it. */
function setCopy(copy: Copy, ref: string, value: unknown) {
  if (value === undefined) {
    delete copy.values[ref]
    delete copy.raw[ref]
    return
  }
  copy.values[ref] = canonical(value)
  copy.raw[ref] = value
}

/** How the server names an item: its kind and key. */
const itemRef = (kind: string, key: string) => `${kind}\u0000${key}`

// Older consoles kept the workspace in this browser, for whoever was signed
// in: it is never taken as anyone's without asking (see legacy).
const storageKey = 'smcp.console.workspace.v1'

export function toEntityInput(e: EntityPartsFragment): EntityInput {
  return {
    name: e.name,
    source: e.source ?? undefined,
    datasource: e.datasource ?? undefined,
    schema: e.schema ?? undefined,
    kind: e.kind ?? undefined,
    description: e.description ?? undefined,
    primaryKey: [...e.primaryKey],
    uniqueKeys: e.uniqueKeys.map((k) => [...k]),
    params: [...e.params],
    affects: [...e.affects],
    allowCascade: e.allowCascade || undefined,
    tenantPolicy: e.tenantPolicy ?? undefined,
    legacyAccess: e.legacyAccess ?? undefined,
    // Field by field: the client adds __typename to what it reads.
    mcp: e.mcp
      ? {
        dmlTools: e.mcp.dmlTools ?? undefined,
        customTool: e.mcp.customTool ?? undefined,
        trustedProcedure: e.mcp.trustedProcedure ?? undefined,
      }
      : undefined,
    fields: e.fields.map((f) => ({
      name: f.name,
      alias: f.alias ?? undefined,
      description: f.description ?? undefined,
      mask: f.mask ?? undefined,
      exclude: f.exclude ?? undefined,
    })),
    relationships: e.relationships.map((r) => ({
      name: r.name, target: r.target, cardinality: r.cardinality, joinOn: { ...r.joinOn },
    })),
  }
}

/**
 * Sections an older console kept, as the workspace holds them now. There a
 * user without a token hash kept the base user's token; here such a user
 * has none: the hash is taken from the base (baseTokens), and a revoked or
 * new token stays as it is.
 */
export function upgradeLegacySections(old: Sections, baseTokens: Record<string, string>): Sections {
  const users = old.users.map((u) =>
    u.tokenHash == null && baseTokens[u.name] ? { ...u, tokenHash: baseTokens[u.name] } : u)
  return { ...old, users }
}

/** The sections of a loaded revision (see Revision.sections), a copy to edit. */
export function revisionSections(rev: NonNullable<WorkspaceResult['revision']>): Sections {
  return JSON.parse(JSON.stringify(rev.sections)) as Sections
}

function sections(s: State): Sections {
  return { entities: s.entities, roles: s.roles, users: s.users, settings: s.settings }
}

/**
 * Canonical JSON for change detection: unset optional text is omitted and
 * object keys are sorted, so the same content compares equal however it was
 * built (the server keeps a workspace with its keys sorted).
 */
// The values whose zero (empty text, false, null) means unset, by path from
// an item's root: the server's merge rules (x/admin/configapp), so items
// compare here as they do there. A revoked token (an empty hash) is a user
// without one; a field excluded: false, one not excluded.
const unset = new Set(mergeSchema.unset)

function normalize(v: unknown, path = ''): unknown {
  if (unset.has(path) && (v === '' || v === false || v === null)) return undefined
  if (Array.isArray(v)) return v.map((x) => normalize(x, `${path}[]`))
  if (typeof v !== 'object' || v === null) return v
  const out: Record<string, unknown> = {}
  for (const k of Object.keys(v).sort()) {
    const n = normalize((v as Record<string, unknown>)[k], path ? `${path}.${k}` : k)
    if (n !== undefined) out[k] = n
  }
  return out
}

/** An item's canonical JSON. */
export function canonical(value: unknown): string {
  return JSON.stringify(normalize(value) ?? null)
}

// The canonical JSON of each item of the store, kept until the item changes:
// change detection compares every item after each edit, and an edit changes
// one. A computed tracks what it read, so an item edited in place is
// computed again, and only it.
const kept = new WeakMap<object, ComputedRef<string>>()

/** canonical(value), kept for an item of the store (a reactive one). */
function itemCanonical(value: unknown): string {
  if (typeof value !== 'object' || value === null || !isReactive(value)) return canonical(value)
  let c = kept.get(toRaw(value))
  if (!c) {
    c = computed(() => canonical(value))
    kept.set(toRaw(value), c)
  }
  return c.value
}

/** A key of the content of sections: equal when their items are. */
function canonicalSections(s: Sections): string {
  const list = (items: unknown[]) => items.map(itemCanonical).join(',')
  return `[${list(s.entities)}]\u0000[${list(s.roles)}]\u0000[${list(s.users)}]\u0000${itemCanonical(s.settings)}`
}

/** How a change names an item: an entity by its ID, others by name. */
function keyOf(kind: Change['kind'], item: { name: string }): string {
  return kind === 'entity' ? entityId(item) : item.name
}

const sectionOf = { entity: 'entities', role: 'roles', user: 'users' } as const

/** Every item of sections by itemRef, as written. */
function itemsOf(s: Sections): Map<string, { kind: Change['kind']; key: string; value: unknown }> {
  const out = new Map<string, { kind: Change['kind']; key: string; value: unknown }>()
  for (const [kind, list] of [['entity', s.entities], ['role', s.roles], ['user', s.users]] as const) {
    for (const item of list) out.set(itemRef(kind, keyOf(kind, item)), { kind, key: keyOf(kind, item), value: item })
  }
  out.set(itemRef('settings', 'settings'), { kind: 'settings', key: 'settings', value: s.settings })
  return out
}

/** An item as the server answers it (see Workspace.items). */
interface ServerItem {
  kind: string
  key: string
  value?: unknown
}

const settingsRef = itemRef('settings', 'settings')

/** The items the server answered, by itemRef; settings unset when it has none. */
function itemsOfList(list: ServerItem[]): Map<string, { kind: Change['kind']; key: string; value: unknown }> {
  const out = new Map<string, { kind: Change['kind']; key: string; value: unknown }>()
  for (const it of list) out.set(itemRef(it.kind, it.key), { kind: it.kind as Change['kind'], key: it.key, value: it.value })
  if (!out.has(settingsRef)) out.set(settingsRef, { kind: 'settings', key: 'settings', value: {} })
  return out
}

/** Items by name, the first of a name (a rename may name two alike while edited). */
function byName<T extends { name: string }>(items: T[]): Map<string, T> {
  const out = new Map<string, T>()
  for (const it of items) if (!out.has(it.name)) out.set(it.name, it)
  return out
}

/** The sections of a document. */
function pickSections(d: Sections): Sections {
  return { entities: d.entities ?? [], roles: d.roles ?? [], users: d.users ?? [], settings: d.settings ?? {} }
}

function byKey<T extends { name: string }>(kind: Change['kind'], items: T[]): Map<string, string> {
  return new Map(items.map((i) => [keyOf(kind, i), itemCanonical(i)]))
}

/** The baseline as compared: its sections, and each item's canonical JSON by kind and key. */
interface Baseline {
  json: string
  sections: Sections
  items: Record<'entity' | 'role' | 'user', Map<string, string>>
}

// The baseline last compared against: parsed once, not on every edit.
let lastBaseline: Baseline | null = null

function baselineOf(json: string): Baseline {
  if (lastBaseline?.json !== json) {
    const sections = JSON.parse(json) as Sections
    lastBaseline = { json, sections, items: {
      entity: byKey('entity', sections.entities), role: byKey('role', sections.roles), user: byKey('user', sections.users),
    } }
  }
  return lastBaseline
}

function diffNamed<T extends { name: string }>(kind: Change['kind'], base: T[] | Map<string, string>, now: T[]):
  Change[] {
  const before = base instanceof Map ? base : byKey(kind, base)
  const after = byKey(kind, now)
  const out: Change[] = []
  for (const [name, json] of after) {
    if (!before.has(name)) out.push({ kind, name, type: 'added' })
    else if (before.get(name) !== json) out.push({ kind, name, type: 'modified' })
  }
  for (const name of before.keys()) if (!after.has(name)) out.push({ kind, name, type: 'removed' })
  return out
}

/** How many items (by kind and name, and the settings) differ between two sets of sections. */
export const itemsChanged = (before: Sections, after: Sections) => changesBetween(before, after).length

/** Item changes from base to now. */
function changesBetween(base: Sections | Baseline, now: Sections): Change[] {
  const items = 'json' in base ? base.items : null
  const sections = 'json' in base ? base.sections : base
  const out = [
    ...diffNamed('entity', items?.entity ?? sections.entities, now.entities),
    ...diffNamed('role', items?.role ?? sections.roles, now.roles),
    ...diffNamed('user', items?.user ?? sections.users, now.users),
  ]
  if (JSON.stringify(sections.settings) !== JSON.stringify(now.settings)) {
    out.push({ kind: 'settings', name: 'settings', type: 'modified' })
  }
  return out
}

/**
 * The workspace's round trips to the server (taking a draft, reading or
 * writing the workspace kept there), one at a time: each reads what it needs,
 * then merges with the workspace as it is at that moment in one synchronous
 * step, so neither another round trip nor an edit made meanwhile is lost.
 */
let lane: Promise<unknown> = Promise.resolve()
export function serially<T>(fn: () => Promise<T>): Promise<T> {
  const next = lane.then(fn, fn)
  lane = next.catch(() => undefined)
  return next
}

/**
 * The workspace's lifetime as one administrator's (see bindTo): an answer
 * to a request made for another's is never written into it.
 */
let lifetime = 0

/** A request's answer arrived after the workspace became another administrator's. */
export class WorkspaceSwitched extends Error {
  constructor() {
    super('the console signed in as another administrator meanwhile; nothing was changed')
  }
}

/** The workspace's lifetime now (see owned). */
export const currentLifetime = () => lifetime

/**
 * request's answer, made and taken only while the workspace is still the
 * one the operation was asked for in (at: its lifetime then, which a
 * queued operation runs well after); otherwise WorkspaceSwitched, the
 * request not even made when the switch came first.
 */
export async function owned<T>(request: () => Promise<T>, at = lifetime): Promise<T> {
  if (lifetime !== at) throw new WorkspaceSwitched()
  const answer = await request()
  if (lifetime !== at) throw new WorkspaceSwitched()
  return answer
}

export const useWorkspace = defineStore('workspace', {
  state: (): State => ({
    owner: '',
    baseId: null,
    basePublished: null,
    baseHash: '',
    datasources: [],
    baseTokens: {},
    baseline: '',
    version: 0,
    copy: null,
    conflicts: [],
    notes: [],
    taken: [],
    checkpoint: null,
    loading: false,
    renamed: {},
    entities: [],
    roles: [],
    users: [],
    settings: {},
  }),

  getters: {
    changes(state): Change[] {
      if (!state.baseline) return []
      return changesBetween(baselineOf(state.baseline), sections(state))
    },
    dirty(): boolean {
      return this.changes.length > 0
    },
    /** A key of the current content, as snapshot() records it. */
    content(state): string {
      return canonicalSections(sections(state))
    },
    /** The draft saved from the workspace while nothing changed since, or null. */
    savedDraftId(state): string | null {
      return this.dirty ? state.checkpoint : null
    },
    /**
     * Changes whenever what is computed on the workspace (catalog status,
     * scope counts, plan previews, simulations) could change: its base or
     * its content.
     */
    contextKey(state): string {
      return `${state.baseId ?? ''}\u0000${this.content}`
    },
    /** The conflicts to resolve, as the server holds them. */
    openConflicts: (state): Conflict[] => state.conflicts,
    /** The entities by ID and by reference (see EntityIndex). */
    entityIndex: (state) => new EntityIndex(state.entities),
    /** The entity with an ID. */
    entity(): (id: string) => EntityInput | undefined {
      return (id) => this.entityIndex.get(id)
    },
    roleIndex: (state) => byName(state.roles),
    userIndex: (state) => byName(state.users),
    role(): (name: string) => RoleInput | undefined {
      return (name) => this.roleIndex.get(name)
    },
    user(): (name: string) => UserInput | undefined {
      return (name) => this.userIndex.get(name)
    },
    /** The roles and users holding a grant on each entity, by its ID, in their order. */
    grantHolders(state): Map<string, { roles: string[]; users: string[] }> {
      const out = new Map<string, { roles: string[]; users: string[] }>()
      const add = (list: { name: string; grants?: GrantInput[] | null }[], side: 'roles' | 'users') => {
        for (const holder of list) {
          for (const id of new Set((holder.grants ?? []).map((g) => this.entityIndex.idOf(g.entity)))) {
            if (id === undefined) continue
            if (!out.has(id)) out.set(id, { roles: [], users: [] })
            out.get(id)![side].push(holder.name)
          }
        }
      }
      add(state.roles, 'roles')
      add(state.users, 'users')
      return out
    },
    /** Roles and users holding a grant on the entity with an ID. */
    accessTo(): (entity: string) => { roles: string[]; users: string[] } {
      return (entity) => this.grantHolders.get(entity) ?? { roles: [], users: [] }
    },
    hasToken: () => (user: UserInput) => Boolean(user.tokenHash),
  },

  actions: {
    /** Replaces the workspace with a loaded revision as the new baseline. */
    apply(data: WorkspaceResult, id: string) {
      const rev = data.revision
      if (!rev) throw new Error(`revision ${id} not found`)
      const loaded = revisionSections(rev)
      this.baseId = rev.id
      this.basePublished = data.published?.id ?? null
      this.baseHash = rev.contentHash
      this.datasources = rev.config.datasources.map((d) => ({ ...d }))
      this.baseTokens = Object.fromEntries(loaded.users.map((u) => [u.name, u.tokenHash ?? '']))
      this.renamed = {}
      Object.assign(this, loaded)
      this.baseline = JSON.stringify(sections(this.$state))
    },

    /** A copy of the workspace as sections, with its content key. */
    snapshot(): { content: string; sections: string } {
      return { content: canonicalSections(sections(this.$state)), sections: JSON.stringify(sections(this.$state)) }
    },

    /**
     * Takes in the workspace the server answered (w; baseData: its base
     * revision, when it builds on another than this one): an item edited
     * here and not written yet stays as edited (it is written with the value
     * it was edited from, and the server merges); every other item becomes
     * the server's. An answer of the items changed since the copy (not
     * complete) touches only those.
     */
    take(w: WorkspacePartsFragment, baseData?: WorkspaceResult) {
      if (w.items.complete) this.takeAll(w, baseData)
      else if (this.copy && !baseData) this.takeChanged(w.items.items)
      else throw new Error('the items changed since a version were answered for a workspace not held at it')
      Object.assign(this, { version: w.version, conflicts: w.conflicts, notes: w.notes, taken: w.taken,
        checkpoint: w.checkpoint ?? null })
    },

    /** take for the items changed since the copy, each replaced where it is. */
    takeChanged(changed: ServerItem[]) {
      const copy = this.copy!
      const lists = { entity: this.entities, role: this.roles, user: this.users }
      const indexes: Partial<Record<keyof typeof lists, Map<string, number>>> = {}
      const at = (kind: keyof typeof lists) =>
        (indexes[kind] ??= new Map(lists[kind].map((x, i) => [keyOf(kind, x), i])))
      const gone = { entity: new Set<number>(), role: new Set<number>(), user: new Set<number>() }
      for (const it of changed) {
        const ref = itemRef(it.kind, it.key)
        const kind = it.kind as Change['kind']
        const i = kind === 'settings' ? undefined : at(kind).get(it.key)
        const mine = kind === 'settings' ? this.settings : i === undefined ? undefined : lists[kind][i]
        // Edited here and not written yet: stays as edited (see take).
        if (itemCanonical(mine) !== (copy.values[ref] ?? canonical(undefined))) continue
        const value = it.value ?? undefined
        setCopy(copy, ref, value)
        const own = value === undefined ? undefined : JSON.parse(JSON.stringify(value))
        if (kind === 'settings') this.settings = own ?? {}
        else if (i !== undefined && own) lists[kind].splice(i, 1, own)
        else if (i !== undefined) gone[kind].add(i)
        else if (own) lists[kind].push(own)
      }
      if (gone.entity.size) this.entities = this.entities.filter((_, i) => !gone.entity.has(i))
      if (gone.role.size) this.roles = this.roles.filter((_, i) => !gone.role.has(i))
      if (gone.user.size) this.users = this.users.filter((_, i) => !gone.user.has(i))
    },

    /** take for every item of the workspace. */
    takeAll(w: WorkspacePartsFragment, baseData?: WorkspaceResult) {
      const was = this.copy
      const pending = new Map<string, { kind: Change['kind']; key: string; value: unknown } | undefined>()
      if (was) {
        const mine = itemsOf(sections(this.$state))
        for (const ref of new Set([...mine.keys(), ...Object.keys(was.values)])) {
          const it = mine.get(ref)
          if (itemCanonical(it?.value) !== (was.values[ref] ?? canonical(undefined))) pending.set(ref, it)
        }
      }
      if (baseData) this.apply(baseData, w.base)
      const server = itemsOfList(w.items.items)
      const copy: Copy = { values: {}, raw: {} }
      for (const [ref, it] of server) setCopy(copy, ref, it.value)
      const next: Sections = { entities: [], roles: [], users: [], settings: {} }
      const put = (it: { kind: Change['kind']; value: unknown }) => {
        if (it.kind === 'settings') next.settings = it.value as Record<string, unknown>
        else (next[sectionOf[it.kind]] as unknown[]).push(it.value)
      }
      for (const [ref, it] of server) {
        if (pending.has(ref)) {
          const mine = pending.get(ref)
          if (mine) put(mine)
        } else put(it)
      }
      for (const [ref, mine] of pending) {
        if (mine && !server.has(ref)) put(mine)
        // Still measured from what it was edited from.
        setCopy(copy, ref, was?.values[ref] === undefined ? undefined : was.raw[ref])
      }
      this.$patch((st) => Object.assign(st, JSON.parse(JSON.stringify(next)) as Sections))
      // Not reactive: nothing renders it, and as many objects again as the
      // items would be watched for nothing.
      this.copy = markRaw(copy)
    },

    /**
     * The items edited here since the copy, each with the value it was edited
     * from (null: absent), and the key one renamed here had.
     */
    unsavedItems(): { kind: string; key: string; value: unknown; previous: unknown; renamedFrom?: string }[] {
      const copy = this.copy
      if (!copy) return []
      const mine = itemsOf(sections(this.$state))
      const out = []
      for (const ref of new Set([...mine.keys(), ...Object.keys(copy.values)])) {
        const m = mine.get(ref)
        if (itemCanonical(m?.value) === (copy.values[ref] ?? canonical(undefined))) continue
        const [kind, key] = ref.split('\u0000') as [string, string]
        // A copy: the item may be edited again while it is being written.
        const value = m ? JSON.parse(JSON.stringify(m.value)) : null
        const from = m && this.renamed[ref] !== key ? this.renamed[ref] : undefined
        out.push({ kind, key, value, previous: copy.raw[ref] ?? null, ...(from ? { renamedFrom: from } : {}) })
      }
      return out
    },

    /**
     * Records in the copy the items a write sent, before taking in the
     * answer: an item edited again while the write was in flight then still
     * differs, and is written next, from what was sent.
     */
    wrote(items: { kind: string; key: string; value: unknown; renamedFrom?: string }[]) {
      const copy = this.copy
      if (!copy) return
      for (const it of items) {
        const ref = itemRef(it.kind, it.key)
        setCopy(copy, ref, it.value ?? undefined)
        if (it.renamedFrom) {
          delete this.renamed[ref]
          // The server knows the item by this key now: one renamed from it
          // while the write was in flight is renamed from this key.
          for (const [other, origin] of Object.entries(this.renamed)) {
            if (origin === it.renamedFrom && other.startsWith(it.kind + '\u0000')) this.renamed[other] = it.key
          }
        }
        if (this.renamed[ref] === it.key) delete this.renamed[ref]
      }
    },

    /**
     * Records that the item of kind once keyed from is now keyed to, when
     * the server knows it by its first key (one added here and renamed
     * before it was written is just written under its new key). Renamed
     * back to that key, the item is recorded as renamed to itself: a write
     * renaming it that is in flight then gives it the key to rename it from.
     */
    rename(kind: 'entity' | 'role', from: string, to: string) {
      if (from === to) return
      const origin = this.renamed[itemRef(kind, from)] ?? from
      delete this.renamed[itemRef(kind, from)]
      if (this.copy?.values[itemRef(kind, origin)] !== undefined) {
        this.renamed[itemRef(kind, to)] = origin
      }
    },

    /**
     * Makes this the workspace of username: another administrator's (one
     * signed in before in this browser) is dropped, to read username's from
     * the server. Returns whether it changed.
     */
    bindTo(username: string): boolean {
      if (this.owner === username) return false
      lifetime++
      this.$reset()
      this.owner = username
      return true
    },

    /**
     * The workspace an older console kept in this browser for whoever was
     * signed in (no owner recorded), when it holds unpublished changes: its
     * base and how many. One without changes is dropped, nothing to lose.
     */
    legacy(): { base: string; changes: number } | null {
      try {
        const raw = localStorage.getItem(storageKey)
        if (!raw) return null
        const saved = JSON.parse(raw) as Partial<State>
        const changes = saved.baseId && saved.baseline
          ? itemsChanged(JSON.parse(saved.baseline) as Sections, pickSections(saved as Sections))
          : 0
        if (changes) return { base: saved.baseId!, changes }
        localStorage.removeItem(storageKey)
      } catch {
        // unreadable: nothing to offer
      }
      return null
    },

    /**
     * The older console's workspace (see legacy): its base and its
     * sections, for its owner to take in as edits on that base.
     */
    legacyWorkspace(): { base: string; sections: Sections } | null {
      try {
        const saved = JSON.parse(localStorage.getItem(storageKey) ?? 'null') as Partial<State> | null
        return saved?.baseId ? { base: saved.baseId, sections: pickSections(saved as Sections) } : null
      } catch {
        return null
      }
    },

    /** Drops the older console's workspace (see legacy). */
    dropLegacy() {
      localStorage.removeItem(storageKey)
    },

    /**
     * Restores one item to its baseline state. Reverting an addition removes
     * the item the same way deleting it would; references elsewhere that the
     * original change touched are separate changes and revert separately.
     */
    revert(change: Pick<Change, 'kind' | 'name'>) {
      if (!this.baseline) return
      const base = JSON.parse(this.baseline) as Sections
      if (change.kind === 'settings') return this.setSettings(base.settings)
      const restore = <T extends { name: string }>(items: T[], baseItems: T[]) => {
        const original = baseItems.find((x) => keyOf(change.kind, x) === change.name)
        const i = items.findIndex((x) => keyOf(change.kind, x) === change.name)
        if (!original) return false
        if (i >= 0) items.splice(i, 1, original)
        else items.splice(Math.min(baseItems.indexOf(original), items.length), 0, original)
        return true
      }
      const restored = change.kind === 'entity' ? restore(this.entities, base.entities)
        : change.kind === 'role' ? restore(this.roles, base.roles)
          : restore(this.users, base.users)
      if (!restored) {
        if (change.kind === 'entity') this.removeEntity(change.name)
        else if (change.kind === 'role') this.removeRole(change.name)
        else this.removeUser(change.name)
      }
    },

    /**
     * Adds imported entities, keeping only relationships whose target exists,
     * and grants them to the named roles (created when missing). Candidates
     * target each other by ID; their references are written in the shortest
     * unambiguous form. Returns how many relationships were dropped.
     */
    importEntities(entities: EntityInput[], roles: string[] = [], actions: Action[] = []): number {
      const added = new Set(entities.map(entityId))
      const targets = new EntityIndex([...this.entities.filter((e) => !added.has(entityId(e))), ...entities])
      // What the import writes is resolved against the entities after it;
      // existing references it makes ambiguous are qualified by the server.
      const relationships = entities.map((e) => (e.relationships ?? []).flatMap((r) => {
        const id = targets.idOf(r.target)
        return id === undefined ? [] : [{ ...r, target: id }]
      }))
      const dropped = entities.reduce((n, e, i) => n + (e.relationships?.length ?? 0) - relationships[i].length, 0)
      this.entities = [
        ...this.entities.filter((e) => !added.has(entityId(e))), ...entities.map((e) => ({ ...e, relationships: [] })),
      ]
      const after = this.entityIndex
      for (const [i, e] of entities.entries()) {
        this.entity(entityId(e))!.relationships = relationships[i].map((r) => ({ ...r, target: after.shortName(r.target) }))
      }
      if (actions.length) {
        for (const name of roles) {
          const role = this.role(name) ?? { name, grants: [], permissions: [] }
          const grants = [
            ...(role.grants ?? []), ...entities.map((e) => ({ entity: after.shortName(entityId(e)), actions: [...actions] })),
          ]
          const i = this.roles.findIndex((r) => r.name === name)
          if (i >= 0) this.roles.splice(i, 1, { ...role, grants })
          else this.roles.push({ ...role, grants })
        }
      }
      return dropped
    },

    /**
     * Adds or replaces an entity. Renaming it or moving it to another
     * datasource or schema (previousId differs) is written as such: the
     * server keeps the table it reads and moves the references to it.
     */
    upsertEntity(e: EntityInput, previousId = entityId(e)) {
      const i = this.entities.findIndex((x) => entityId(x) === previousId)
      if (i >= 0) this.entities.splice(i, 1, e)
      else this.entities.push(e)
      this.rename('entity', previousId, entityId(e))
    },

    /**
     * Removes an entity; the server drops the grants on it, the
     * relationships to it and its place in procedures' affects.
     */
    removeEntity(id: string) {
      this.removeEntities([id])
    },

    /** Removes entities at once (see removeEntity). */
    removeEntities(ids: string[]) {
      const gone = new Set(ids)
      this.entities = this.entities.filter((e) => !gone.has(entityId(e)))
      for (const id of ids) delete this.renamed[itemRef('entity', id)]
    },

    /**
     * Adds and removes fields of an entity; the server drops removed ones
     * from the field lists of grants on it. Relationships, keys, row filters
     * and the tenant policy that name them are the caller's to show first
     * (see the fieldReferences query): the server rejects them.
     */
    syncFields(entity: string, added: FieldInput[], removed: string[]) {
      const e = this.entity(entity)
      if (!e) return
      const gone = new Set(removed)
      const fields = (e.fields ?? []).filter((f) => !gone.has(f.name))
      this.upsertEntity({ ...e, fields: [...fields, ...added.filter((f) => !fields.some((x) => x.name === f.name))] })
    },

    /** Adds or replaces a role; renaming it, the server renames it on its members. */
    upsertRole(r: RoleInput, previousName = r.name) {
      const i = this.roles.findIndex((x) => x.name === previousName)
      if (i >= 0) this.roles.splice(i, 1, r)
      else this.roles.push(r)
      this.rename('role', previousName, r.name)
    },

    /** Removes a role; the server takes it away from every user. */
    removeRole(name: string) {
      this.roles = this.roles.filter((r) => r.name !== name)
      delete this.renamed[itemRef('role', name)]
    },

    /** Sets which users hold a role. */
    setRoleMembers(role: string, members: string[]) {
      for (const u of this.users) {
        const has = (u.roles ?? []).includes(role)
        const want = members.includes(u.name)
        if (want && !has) u.roles = [...(u.roles ?? []), role]
        if (!want && has) u.roles = (u.roles ?? []).filter((x) => x !== role)
      }
    },

    upsertUser(u: UserInput, previousName = u.name) {
      const i = this.users.findIndex((x) => x.name === previousName)
      if (i >= 0) this.users.splice(i, 1, u)
      else this.users.push(u)
    },

    removeUser(name: string) {
      this.users = this.users.filter((u) => u.name !== name)
    },

    setSettings(settings: Record<string, unknown>) {
      this.settings = settings
    },
  },
})
