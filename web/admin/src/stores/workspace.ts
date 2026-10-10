import { defineStore } from 'pinia'
import { EntityIndex, entityId } from '@/lib/entityRefs'
import { filterFields, type Filter } from '@/lib/filter'
import type {
  Action,
  FieldInput,
  WorkspaceQuery as WorkspaceResult,
  DraftInput,
  EntityInput,
  EntityPartsFragment,
  GrantInput,
  GrantPartsFragment,
  PreviewPlanQuery,
  RoleInput,
  UserInput,
  WorkspacePartsFragment,
} from '@/gql/graphql'

export interface Datasource {
  name: string
  driver: string
  dsn: string
  connections: { name: string; dsn: string; role: string; pooler?: string | null }[]
  routing: { read: string; write: string; execute: string }
  readAfterWrite?: string | null
}

/** What the workspace edits, as the server keeps it (see x/admin/configapp). */
export interface Sections {
  entities: EntityInput[]
  roles: RoleInput[]
  users: UserInput[]
  settings: Record<string, unknown>
}

/** A place that names one field of an entity. */
export interface FieldReference {
  kind: 'role' | 'user' | 'relationship' | 'primaryKey' | 'uniqueKey' | 'tenantPolicy'
  /** The role, user or entity holding the reference. */
  name: string
  /**
   * For a role or user: whether a grant lists the field (removing the field
   * removes it there too) or filters rows by it (which must be fixed first).
   */
  via?: 'fields' | 'rows'
}

/** Whether removing a field takes care of a reference to it. */
export const followsRemoval = (r: FieldReference) => r.via === 'fields'

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
    mcp: { dmlTools: e.mcp.dmlTools, customTool: e.mcp.customTool, trustedProcedure: e.mcp.trustedProcedure },
    fields: e.fields.map((f) => ({
      name: f.name,
      alias: f.alias ?? undefined,
      description: f.description ?? undefined,
      mask: f.mask ?? undefined,
      exclude: f.exclude,
    })),
    relationships: e.relationships.map((r) => ({
      name: r.name, target: r.target, cardinality: r.cardinality, joinOn: { ...r.joinOn },
    })),
  }
}

function toGrantInput(g: GrantPartsFragment): GrantInput {
  return {
    entity: g.entity,
    actions: [...g.actions],
    fieldsRestricted: g.fieldsRestricted,
    readFields: [...g.readFields],
    writeFields: [...g.writeFields],
    rows: g.rows ?? undefined,
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

/** The editable sections of a loaded revision. */
export function revisionSections(rev: NonNullable<WorkspaceResult['revision']>): Sections {
  const cfg = rev.config
  return {
    entities: cfg.entities.map(toEntityInput),
    roles: cfg.roles.map((r) => ({
      name: r.name,
      description: r.description ?? undefined,
      grants: r.grants.map(toGrantInput),
    })),
    users: cfg.users.map((u) => ({
      name: u.name,
      description: u.description ?? undefined,
      roles: [...u.roles],
      subject: u.subject ?? undefined,
      disabled: u.disabled,
      tokenHash: u.tokenHash ?? undefined,
      grants: u.grants.map(toGrantInput),
    })),
    settings: { ...(cfg.settings as Record<string, unknown>) },
  }
}

function sections(s: State): Sections {
  return { entities: s.entities, roles: s.roles, users: s.users, settings: s.settings }
}

/**
 * Canonical JSON for change detection: unset optional text is omitted and
 * object keys are sorted, so the same content compares equal however it was
 * built (the server keeps a workspace with its keys sorted).
 */
// Optional text where empty and unset mean the same, as in the server's
// merge schema: a revoked token (an empty hash) is a user without one.
const optionalText = new Set(['source', 'datasource', 'schema', 'kind', 'description', 'alias', 'mask', 'tokenHash'])

function normalize(v: unknown, key = ''): unknown {
  if (optionalText.has(key) && (v === '' || v === null)) return undefined
  if (Array.isArray(v)) return v.map((x) => normalize(x))
  if (typeof v !== 'object' || v === null) return v
  const out: Record<string, unknown> = {}
  for (const k of Object.keys(v).sort()) {
    const n = normalize((v as Record<string, unknown>)[k], k)
    if (n !== undefined) out[k] = n
  }
  return out
}

export function canonical(value: unknown): string {
  return JSON.stringify(normalize(value) ?? null)
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

/** The sections of a document. */
function pickSections(d: Sections): Sections {
  return { entities: d.entities ?? [], roles: d.roles ?? [], users: d.users ?? [], settings: d.settings ?? {} }
}

function byKey<T extends { name: string }>(kind: Change['kind'], items: T[]): Map<string, string> {
  return new Map(items.map((i) => [keyOf(kind, i), canonical(i)]))
}

function diffNamed<T extends { name: string }>(kind: Change['kind'], base: T[], now: T[]): Change[] {
  const before = byKey(kind, base)
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
function changesBetween(base: Sections, now: Sections): Change[] {
  const out = [
    ...diffNamed('entity', base.entities, now.entities),
    ...diffNamed('role', base.roles, now.roles),
    ...diffNamed('user', base.users, now.users),
  ]
  if (JSON.stringify(base.settings) !== JSON.stringify(now.settings)) {
    out.push({ kind: 'settings', name: 'settings', type: 'modified' })
  }
  return out
}

type References = Pick<Sections, 'entities' | 'roles' | 'users'>

/**
 * Rewrites every entity reference (grants, relationship targets, procedure
 * affects): to returns its new form, or null to drop it.
 */
function rewriteReferences(s: References, to: (ref: string) => string | null) {
  const grants = (gs: GrantInput[] | null | undefined) => (gs ?? []).flatMap((g) => {
    const ref = to(g.entity)
    return ref === null ? [] : [ref === g.entity ? g : { ...g, entity: ref }]
  })
  for (const r of s.roles) r.grants = grants(r.grants)
  for (const u of s.users) u.grants = grants(u.grants)
  for (const e of s.entities) {
    if (e.relationships?.length) {
      e.relationships = e.relationships.flatMap((rel) => {
        const ref = to(rel.target)
        return ref === null ? [] : [ref === rel.target ? rel : { ...rel, target: ref }]
      })
    }
    if (e.affects?.length) e.affects = e.affects.flatMap((a) => to(a) ?? [])
  }
}

/**
 * Keeps every reference naming the entity it named before entities changed
 * (before), qualifying it when the change made it ambiguous. moved maps the
 * IDs of renamed entities to their new IDs, and of removed ones to null,
 * whose references are dropped. References that named no single entity
 * before are left as they are.
 */
function retarget(s: References, before: (ref: string) => string | undefined, moved = new Map<string, string | null>()) {
  const after = new EntityIndex(s.entities)
  rewriteReferences(s, (ref) => {
    const was = before(ref)
    if (was === undefined) return ref
    const now = moved.has(was) ? moved.get(was)! : was
    if (now === null) return null
    return after.idOf(ref) === now ? ref : after.shortName(now)
  })
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
    entities: [],
    roles: [],
    users: [],
    settings: {},
  }),

  getters: {
    changes(state): Change[] {
      if (!state.baseline) return []
      return changesBetween(JSON.parse(state.baseline) as Sections, sections(state))
    },
    dirty(): boolean {
      return this.changes.length > 0
    },
    /** A key of the current content, as snapshot() records it. */
    content(state): string {
      return canonical(sections(state))
    },
    /** The draft saved from the workspace while nothing changed since, or null. */
    savedDraftId(state): string | null {
      return this.dirty ? state.checkpoint : null
    },
    /**
     * The workspace as a draft of its base revision, holding only the
     * sections it changed: the server keeps the others from the base, so
     * requests stay small (each check of the review page sends one). A removed
     * settings section is sent as null.
     */
    draft(state): DraftInput {
      const draft: DraftInput = { base: state.baseId ?? '' }
      // The users here are as they are, token included: one without a token
      // hash has none (a missing hash would keep the base user's).
      const users = state.users.map((u) => ({ ...u, tokenHash: u.tokenHash ?? '' }))
      if (!state.baseline) return { ...draft, entities: state.entities, roles: state.roles, users, settings: state.settings }
      const base = JSON.parse(state.baseline) as Sections
      if (canonical(base.entities) !== canonical(state.entities)) draft.entities = state.entities
      if (canonical(base.roles) !== canonical(state.roles)) draft.roles = state.roles
      if (canonical(base.users) !== canonical(state.users)) draft.users = users
      const settings: Record<string, unknown> = {}
      for (const key of new Set([...Object.keys(base.settings), ...Object.keys(state.settings)])) {
        if (canonical(base.settings[key]) !== canonical(state.settings[key])) settings[key] = state.settings[key] ?? null
      }
      if (Object.keys(settings).length) draft.settings = settings
      return draft
    },
    /**
     * What every computation on the workspace (catalog status, scope counts,
     * plan previews, table details) runs against: its base revision with the
     * unsaved changes. Always the workspace, never "the published revision
     * when nothing is unsaved": a loaded draft is a base of its own. Undefined
     * before a revision is loaded.
     */
    computeDraft(): DraftInput | undefined {
      return this.baseId ? this.draft : undefined
    },
    /** Changes whenever computeDraft would compute something else. */
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
    role: (state) => (name: string) => state.roles.find((r) => r.name === name),
    user: (state) => (name: string) => state.users.find((u) => u.name === name),
    /** Roles and users holding a grant on the entity with an ID. */
    accessTo(state): (entity: string) => { roles: string[]; users: string[] } {
      const on = (entity: string) => (g: GrantInput) => this.entityIndex.idOf(g.entity) === entity
      return (entity) => ({
        roles: state.roles.filter((r) => (r.grants ?? []).some(on(entity))).map((r) => r.name),
        users: state.users.filter((u) => (u.grants ?? []).some(on(entity))).map((u) => u.name),
      })
    },
    /**
     * Where roles, users and entities name each field, built in one pass:
     * `${entity ID}\u0000${field}` → references. An entity holding a
     * reference is named by its ID.
     */
    fieldReferenceIndex(state): Map<string, FieldReference[]> {
      const index = this.entityIndex
      const out = new Map<string, FieldReference[]>()
      const add = (entity: string | undefined, field: string, ref: FieldReference) => {
        if (entity === undefined) return
        const key = `${entity}\u0000${field}`
        const list = out.get(key) ?? []
        if (!list.some((r) => r.kind === ref.kind && r.name === ref.name)) out.set(key, [...list, ref])
      }
      for (const [kind, holders] of [['role', state.roles], ['user', state.users]] as const) {
        for (const h of holders) {
          // Row filters first: a field a grant both lists and filters by blocks removal.
          for (const g of h.grants ?? []) {
            const entity = index.idOf(g.entity)
            for (const f of filterFields(g.rows as Filter | undefined)) add(entity, f, { kind, name: h.name, via: 'rows' })
          }
          for (const g of h.grants ?? []) {
            const entity = index.idOf(g.entity)
            for (const f of [...(g.readFields ?? []), ...(g.writeFields ?? [])]) add(entity, f, { kind, name: h.name, via: 'fields' })
          }
        }
      }
      for (const e of state.entities) {
        const id = entityId(e)
        for (const rel of e.relationships ?? []) {
          for (const [local, remote] of Object.entries((rel.joinOn ?? {}) as Record<string, string>)) {
            add(id, local, { kind: 'relationship', name: id })
            add(index.idOf(rel.target), remote, { kind: 'relationship', name: id })
          }
        }
        for (const f of e.primaryKey ?? []) add(id, f, { kind: 'primaryKey', name: id })
        for (const k of e.uniqueKeys ?? []) for (const f of k) add(id, f, { kind: 'uniqueKey', name: id })
        for (const f of filterFields(e.tenantPolicy as Filter | undefined)) add(id, f, { kind: 'tenantPolicy', name: id })
      }
      return out
    },
    /** Where roles, users and entities name a field of the entity with an ID. */
    fieldReferences(): (entity: string, field: string) => FieldReference[] {
      return (entity, field) => {
        const refs = this.fieldReferenceIndex.get(`${entity}\u0000${field}`) ?? []
        // Roles and users first, as the previous listing did.
        const order = { role: 0, user: 1, relationship: 2, primaryKey: 3, uniqueKey: 4, tenantPolicy: 5 }
        return [...refs].sort((a, b) => order[a.kind] - order[b.kind])
      }
    },
    hasToken: () => (user: UserInput) => Boolean(user.tokenHash),
  },

  actions: {
    /** Replaces the workspace with a loaded revision as the new baseline. */
    apply(data: WorkspaceResult, id: string) {
      const rev = data.revision
      if (!rev) throw new Error(`revision ${id} not found`)
      const cfg = rev.config
      this.baseId = rev.id
      this.basePublished = data.published?.id ?? null
      this.baseHash = rev.contentHash
      this.datasources = cfg.datasources.map((d) => ({ ...d }))
      this.baseTokens = Object.fromEntries(cfg.users.map((u) => [u.name, u.tokenHash ?? '']))
      Object.assign(this, revisionSections(rev))
      this.baseline = JSON.stringify(sections(this.$state))
    },

    /** A copy of the workspace as a draft and as sections, with its content key. */
    snapshot(): { draft: DraftInput; content: string; sections: string } {
      const draft = JSON.parse(JSON.stringify(this.draft)) as DraftInput
      return { draft, content: canonical(sections(this.$state)), sections: JSON.stringify(sections(this.$state)) }
    },

    /**
     * Takes in the workspace the server answered (w; baseData: its base
     * revision, when it builds on another than this one): an item edited
     * here and not written yet stays as edited (it is written with the value
     * it was edited from, and the server merges); every other item becomes
     * the server's.
     */
    take(w: WorkspacePartsFragment, baseData?: WorkspaceResult) {
      const was = this.copy
      const pending = new Map<string, { kind: Change['kind']; key: string; value: unknown } | undefined>()
      if (was) {
        const mine = itemsOf(sections(this.$state))
        for (const ref of new Set([...mine.keys(), ...Object.keys(was.values)])) {
          const it = mine.get(ref)
          if (canonical(it?.value) !== (was.values[ref] ?? canonical(undefined))) pending.set(ref, it)
        }
      }
      if (baseData) this.apply(baseData, w.base)
      const server = itemsOf(w.sections as Sections)
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
      Object.assign(this, { copy, version: w.version, conflicts: w.conflicts, notes: w.notes, taken: w.taken,
        checkpoint: w.checkpoint ?? null })
    },

    /** The items edited here since the copy, each with the value it was edited from (null: absent). */
    pendingItems(): { kind: string; key: string; value: unknown; previous: unknown }[] {
      const copy = this.copy
      if (!copy) return []
      const mine = itemsOf(sections(this.$state))
      const out = []
      for (const ref of new Set([...mine.keys(), ...Object.keys(copy.values)])) {
        const m = mine.get(ref)
        if (canonical(m?.value) === (copy.values[ref] ?? canonical(undefined))) continue
        const [kind, key] = ref.split('\u0000') as [string, string]
        // A copy: the item may be edited again while it is being written.
        out.push({ kind, key, value: m ? JSON.parse(JSON.stringify(m.value)) : null, previous: copy.raw[ref] ?? null })
      }
      return out
    },

    /**
     * Records in the copy the items a write sent, before taking in the
     * answer: an item edited again while the write was in flight then still
     * differs, and is written next, from what was sent.
     */
    wrote(items: { kind: string; key: string; value: unknown }[]) {
      const copy = this.copy
      if (!copy) return
      for (const it of items) setCopy(copy, itemRef(it.kind, it.key), it.value ?? undefined)
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
     * unambiguous form, and existing references the new entities make
     * ambiguous are qualified. Returns how many relationships were dropped.
     */
    importEntities(entities: EntityInput[], roles: string[] = [], actions: Action[] = []): number {
      const before = this.entityIndex
      const added = new Set(entities.map(entityId))
      const targets = new EntityIndex([...this.entities.filter((e) => !added.has(entityId(e))), ...entities])
      // Existing references first: those the new entities make ambiguous are
      // qualified. What the import writes is added after, resolved against
      // the entities after the import, never by what a reference meant before.
      const relationships = entities.map((e) => (e.relationships ?? []).flatMap((r) => {
        const id = targets.idOf(r.target)
        return id === undefined ? [] : [{ ...r, target: id }]
      }))
      const dropped = entities.reduce((n, e, i) => n + (e.relationships?.length ?? 0) - relationships[i].length, 0)
      this.entities = [
        ...this.entities.filter((e) => !added.has(entityId(e))), ...entities.map((e) => ({ ...e, relationships: [] })),
      ]
      retarget(this.$state, (ref) => before.idOf(ref))
      const after = this.entityIndex
      for (const [i, e] of entities.entries()) {
        this.entity(entityId(e))!.relationships = relationships[i].map((r) => ({ ...r, target: after.shortName(r.target) }))
      }
      if (actions.length) {
        for (const name of roles) {
          const role = this.role(name) ?? { name, grants: [] }
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
     * datasource or schema (previousId differs) keeps the table it reads
     * (source defaults to the name) and moves every reference along: grants
     * of roles and users, relationships of other entities that target it and
     * procedures that affect it. References the change makes ambiguous are
     * qualified (see retarget).
     */
    upsertEntity(e: EntityInput, previousId = entityId(e)) {
      const before = this.entityIndex
      const previous = before.get(previousId)
      if (previous && previous.name !== e.name && !e.source) e = { ...e, source: previous.source || previous.name }
      const i = this.entities.findIndex((x) => entityId(x) === previousId)
      if (i >= 0) this.entities.splice(i, 1, e)
      else this.entities.push(e)
      retarget(this.$state, (ref) => before.idOf(ref), new Map([[previousId, entityId(e)]]))
    },

    /**
     * Removes an entity, every grant on it, every relationship to it and its
     * place in procedures' affects (an emptied list invalidates the whole
     * datasource, the safe default).
     */
    removeEntity(id: string) {
      const before = this.entityIndex
      this.entities = this.entities.filter((e) => entityId(e) !== id)
      retarget(this.$state, (ref) => before.idOf(ref), new Map([[id, null]]))
    },

    /**
     * Adds and removes fields of an entity. Removed fields also leave the
     * read and write lists of grants on it; relationships, the primary key,
     * row scopes and the tenant policy that name them are left for the
     * caller to show (see fieldReferences), since the server rejects them.
     */
    syncFields(entity: string, added: FieldInput[], removed: string[]) {
      const e = this.entity(entity)
      if (!e) return
      const gone = new Set(removed)
      const fields = (e.fields ?? []).filter((f) => !gone.has(f.name))
      this.upsertEntity({ ...e, fields: [...fields, ...added.filter((f) => !fields.some((x) => x.name === f.name))] })
      const index = this.entityIndex
      const prune = (g: GrantInput) => (index.idOf(g.entity) !== entity ? g : {
        ...g,
        readFields: (g.readFields ?? []).filter((f) => !gone.has(f)),
        writeFields: (g.writeFields ?? []).filter((f) => !gone.has(f)),
      })
      for (const r of this.roles) r.grants = (r.grants ?? []).map(prune)
      for (const u of this.users) u.grants = (u.grants ?? []).map(prune)
    },

    /**
     * Takes a plan's outcome (see previewPlan) into the workspace, like an
     * edit: the entities it adds are imported (references they make
     * ambiguous are qualified), the entities it changes take its fields, and
     * the roles and users it changes take its grants, which name entities by
     * ID and are written in their shortest unambiguous form.
     */
    upsertRole(r: RoleInput, previousName = r.name) {
      const i = this.roles.findIndex((x) => x.name === previousName)
      if (i >= 0) this.roles.splice(i, 1, r)
      else this.roles.push(r)
      if (previousName !== r.name) {
        for (const u of this.users) u.roles = (u.roles ?? []).map((x) => (x === previousName ? r.name : x))
      }
    },

    /** Removes a role and takes it away from every user. */
    removeRole(name: string) {
      this.roles = this.roles.filter((r) => r.name !== name)
      for (const u of this.users) u.roles = (u.roles ?? []).filter((x) => x !== name)
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
