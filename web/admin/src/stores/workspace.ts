import { defineStore } from 'pinia'
import { run } from '@/api/client'
import { PublishedQuery, RevisionHashQuery, WorkspaceQuery } from '@/api/ops'
import { EntityIndex, entityId } from '@/lib/entityRefs'
import { filterFields, type Filter } from '@/lib/filter'
import { getAt, merge, same, setAt, type Path } from '@/lib/merge'
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
} from '@/gql/graphql'

export interface Datasource {
  name: string
  driver: string
  dsn: string
  connections: { name: string; dsn: string; role: string; pooler?: string | null }[]
  routing: { read: string; write: string; execute: string }
  readAfterWrite?: string | null
}

/** What the workspace edits; also the shape persisted to localStorage. */
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

/**
 * A value this workspace and a newer published revision changed differently
 * (see lib/merge). The workspace keeps its value meanwhile; it cannot be
 * saved or published until every conflict is resolved.
 */
export interface Conflict {
  kind: Change['kind']
  /** The item, as Change names it. */
  name: string
  /** Where in the item; empty for the whole item. */
  path: Path
  mine: unknown
  /** The value in revision; undefined when removed there. */
  theirs: unknown
  revision: string
}

interface State extends Sections {
  baseId: string | null
  /** The published revision when the workspace was loaded or rebased. */
  basePublished: string | null
  baseHash: string
  datasources: Datasource[]
  /** Users that had a token in the base revision. */
  baseTokens: Record<string, boolean>
  baseline: string
  /** The draft last saved from this workspace and the content it holds. */
  savedDraft: { id: string; content: string } | null
  conflicts: Conflict[]
  loading: boolean
}

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
    mcp: { ...e.mcp },
    fields: e.fields.map((f) => ({
      name: f.name,
      alias: f.alias ?? undefined,
      description: f.description ?? undefined,
      mask: f.mask ?? undefined,
      exclude: f.exclude,
    })),
    relationships: e.relationships.map((r) => ({ ...r, joinOn: { ...r.joinOn } })),
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
      grants: u.grants.map(toGrantInput),
    })),
    settings: { ...(cfg.settings as Record<string, unknown>) },
  }
}

function sections(s: State): Sections {
  return { entities: s.entities, roles: s.roles, users: s.users, settings: s.settings }
}

// Optional text where empty and unset mean the same, as in the server model;
// tokenHash is excluded on purpose: an empty hash revokes the token.
const optionalText = new Set(['source', 'datasource', 'schema', 'kind', 'description', 'alias', 'mask'])

/** Canonical JSON for change detection: unset optional text is omitted. */
export function canonical(value: unknown): string {
  return JSON.stringify(value, (key, v: unknown) => (optionalText.has(key) && (v === '' || v === null) ? undefined : v))
}

/** How a change names an item: an entity by its ID, others by name. */
function keyOf(kind: Change['kind'], item: { name: string }): string {
  return kind === 'entity' ? entityId(item) : item.name
}

/** Whether conflict c is on the value of outer's path or one under it. */
function within(c: Conflict, outer: Conflict): boolean {
  return c.kind === outer.kind && c.name === outer.name && c.path.length >= outer.path.length &&
    same(c.path.slice(0, outer.path.length), outer.path)
}

/**
 * The part of path that still names one value in each of items: a path
 * through a list element that has since become a group (a second grant on
 * one entity, say) stops at the group, which the merge treats as a whole.
 */
function addressable(path: Path, ...items: unknown[]): Path {
  for (let i = 0; i < path.length - 1; i++) {
    if (typeof path[i] !== 'string' && items.some((item) => Array.isArray(getAt(item, path.slice(0, i + 1))))) {
      return path.slice(0, i + 1)
    }
  }
  return path
}

/**
 * A conflict as it stands in the workspace s, against base, the newest
 * published revision: its path (see addressable), the workspace's value and
 * the published one; null when there is nothing left to choose, because the
 * workspace holds the published value already (for instance typed in by
 * hand) or has removed what holds it, the item or a list element or object
 * on the way (a removal made on the newer revision overwrites nothing unseen,
 * and taking theirs would rebuild it without its name or key). Values are compared in place, so unset optional text
 * equals empty text.
 */
function view(s: Sections, base: Sections, c: Conflict): Conflict | null {
  const [item, theirsItem] = [pick(s, c), pick(base, c)]
  const path = addressable(c.path, item, theirsItem)
  // The workspace removed what holds the value (the item, a list element or
  // an object on the way).
  for (let i = 0; i < path.length; i++) if (getAt(item, path.slice(0, i)) === undefined) return null
  const [mine, theirs] = [getAt(item, path), getAt(theirsItem, path)]
  const last = path[path.length - 1]
  const settled = typeof last === 'string' ? same({ [last]: mine }, { [last]: theirs }) : same(mine, theirs)
  return settled ? null : { ...c, path, mine, theirs }
}

/** The item a change refers to in sections, or undefined when absent. */
function pick(s: Sections, c: Pick<Change, 'kind' | 'name'>): unknown {
  if (c.kind === 'settings') return s.settings
  const items: { name: string }[] = c.kind === 'entity' ? s.entities : c.kind === 'role' ? s.roles : s.users
  return items.find((x) => keyOf(c.kind, x) === c.name)
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

export const useWorkspace = defineStore('workspace', {
  state: (): State => ({
    baseId: null,
    basePublished: null,
    baseHash: '',
    datasources: [],
    baseTokens: {},
    baseline: '',
    savedDraft: null,
    conflicts: [],
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
    /** The saved draft holding exactly the unsaved changes, or null. */
    savedDraftId(state): string | null {
      return this.dirty && state.savedDraft?.content === canonical(sections(state)) ? state.savedDraft.id : null
    },
    /**
     * The workspace as a draft of its base revision, holding only the
     * sections it changed: the server keeps the others from the base, so
     * requests stay small (each check of the review page sends one). A removed
     * settings section is sent as null.
     */
    draft(state): DraftInput {
      const draft: DraftInput = { base: state.baseId ?? '' }
      if (!state.baseline) return { ...draft, entities: state.entities, roles: state.roles, users: state.users, settings: state.settings }
      const base = JSON.parse(state.baseline) as Sections
      if (canonical(base.entities) !== canonical(state.entities)) draft.entities = state.entities
      if (canonical(base.roles) !== canonical(state.roles)) draft.roles = state.roles
      if (canonical(base.users) !== canonical(state.users)) draft.users = state.users
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
    /**
     * The conflicts still to resolve: those whose value the workspace does
     * not already hold from the newer revision. mine is the workspace's
     * current value, edits since the conflict arose included: it is what
     * keeping mine keeps.
     */
    openConflicts(state): Conflict[] {
      const s = sections(state)
      const base = JSON.parse(state.baseline || '{}') as Sections
      return state.conflicts.flatMap((c) => view(s, base, c) ?? [])
    },
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
    hasToken: (state) => (user: UserInput) =>
      user.tokenHash != null ? user.tokenHash !== '' : Boolean(state.baseTokens[user.name]),
  },

  actions: {
    /** Loads a revision as the new baseline, discarding unsaved changes. */
    async load(id: string) {
      this.loading = true
      try {
        this.apply(await run(WorkspaceQuery, { id }), id)
      } finally {
        this.loading = false
      }
    },

    /**
     * Moves the workspace onto revision id and re-applies the unsaved
     * changes with a three-way merge (see lib/merge). Returns the conflicts
     * found: values id changed that this workspace changed differently,
     * which keep the local value until resolved (see resolveConflict).
     */
    async rebase(id: string, from?: string): Promise<Conflict[]> {
      this.loading = true
      try {
        return this.rebaseOnto(await run(WorkspaceQuery, { id }), id, from)
      } finally {
        this.loading = false
      }
    },

    /**
     * from (a snapshot's sections) replaces the baseline as the point the
     * local edits are measured from: after publishing a snapshot, only edits
     * made since it, including undoing part of it, are re-applied.
     */
    rebaseOnto(data: WorkspaceResult, id: string, from?: string): Conflict[] {
      const oldBase = JSON.parse(from ?? this.baseline) as Sections
      const local = JSON.parse(JSON.stringify(sections(this.$state))) as Sections
      const changes = changesBetween(oldBase, local)
      const pending = this.conflicts
      this.apply(data, id)
      const newBase = JSON.parse(this.baseline) as Sections
      const found: Conflict[] = []
      for (const c of changes) {
        const merged = merge(pick(oldBase, c), pick(local, c), pick(newBase, c))
        found.push(...merged.conflicts.map((v) => ({ kind: c.kind, name: c.name, ...v, revision: id })))
        if (c.kind === 'settings') this.settings = (merged.value ?? {}) as Record<string, unknown>
        else if (merged.value === undefined) this.removeItem(c.kind, c.name)
        else this.upsertItem(c.kind, merged.value as never, c.name)
      }
      // Conflicts not resolved before stand against the newest revision's
      // value, unless it now equals the local one.
      const now = sections(this.$state)
      // A new conflict replaces those under it (a removed item replaces the
      // conflicts on its values); one under an older, still unresolved
      // conflict is left to it, which now stands against the newest value of
      // its whole path.
      const carried = pending.flatMap((p) => {
        if (found.some((f) => within(p, f))) return []
        return view(now, newBase, { ...p, revision: id }) ?? []
      })
      const added = found.filter((f) => !carried.some((p) => within(f, p)))
      this.conflicts = [...carried, ...added]
      this.persist()
      return added
    },

    /** Adds or replaces an item a change names. */
    upsertItem(kind: Change['kind'], item: never, previous: string) {
      if (kind === 'entity') this.upsertEntity(item, previous)
      else if (kind === 'role') this.upsertRole(item, previous)
      else if (kind === 'user') this.upsertUser(item, previous)
    },

    /** Removes an item a change names. */
    removeItem(kind: Change['kind'], name: string) {
      if (kind === 'entity') this.removeEntity(name)
      else if (kind === 'role') this.removeRole(name)
      else if (kind === 'user') this.removeUser(name)
    },

    /**
     * Resolves a conflict: mine keeps the workspace's current value, theirs
     * takes the value of the newer revision (removing it when removed there).
     */
    resolveConflict(conflict: Conflict, choice: 'mine' | 'theirs') {
      if (choice === 'theirs') {
        if (conflict.kind === 'settings') {
          this.settings = (setAt(this.settings, conflict.path, conflict.theirs) ?? {}) as Record<string, unknown>
        } else {
          const item = setAt(pick(sections(this.$state), conflict), conflict.path, conflict.theirs)
          if (item === undefined) this.removeItem(conflict.kind, conflict.name)
          else this.upsertItem(conflict.kind, item as never, conflict.name)
        }
      }
      // Its choice decides the values under it too, and what now matches the
      // newer revision needs no choice.
      const now = sections(this.$state)
      const base = JSON.parse(this.baseline) as Sections
      this.conflicts = this.conflicts.flatMap((c) => (within(c, conflict) ? [] : view(now, base, c) ?? []))
      this.persist()
    },

    /** Replaces the workspace with a loaded revision as the new baseline. */
    apply(data: WorkspaceResult, id: string) {
      const rev = data.revision
      if (!rev) throw new Error(`revision ${id} not found`)
      const cfg = rev.config
      this.baseId = rev.id
      this.basePublished = data.published?.id ?? null
      this.baseHash = rev.contentHash
      this.datasources = cfg.datasources.map((d) => ({ ...d }))
      this.baseTokens = Object.fromEntries(cfg.users.map((u) => [u.name, u.hasToken]))
      Object.assign(this, revisionSections(rev))
      this.baseline = JSON.stringify(sections(this.$state))
      this.savedDraft = null
      this.conflicts = []
      this.persist()
    },

    /**
     * A copy of the workspace to submit, with its content key. Saving binds
     * the draft id to this content, so edits made while the request is in
     * flight stay unsaved.
     */
    snapshot(): { draft: DraftInput; content: string; sections: string } {
      const draft = JSON.parse(JSON.stringify(this.draft)) as DraftInput
      return { draft, content: canonical(sections(this.$state)), sections: JSON.stringify(sections(this.$state)) }
    },

    /** Records that draft id holds content (from snapshot). */
    markDraftSaved(id: string, content: string) {
      this.savedDraft = { id, content }
      this.persist()
    },

    /** Loads the published revision; returns false when nothing is published. */
    async loadPublished(): Promise<boolean> {
      const data = await run(PublishedQuery, {})
      if (!data.published) return false
      await this.load(data.published.id)
      return true
    },

    /**
     * Checks a restored workspace against the server: when its base revision
     * no longer exists or holds other content (the store was replaced), the
     * base is reloaded, re-applying unsaved changes onto it. Returns the
     * revision the workspace was moved onto and the conflicting changes, or
     * null when the base is current.
     */
    async verifyBase(): Promise<{ id: string; conflicts: Conflict[] } | null> {
      if (!this.baseId) return null
      const { revision } = await run(RevisionHashQuery, { id: this.baseId })
      if (revision?.contentHash === this.baseHash) {
        // Datasources are read-only here and not persisted: take the server's.
        this.datasources = revision.config.datasources.map((d) => ({ ...d }))
        return null
      }
      const target = revision?.id ?? (await run(PublishedQuery, {})).published?.id
      if (!target) return null
      if (!this.dirty) {
        await this.load(target)
        return { id: target, conflicts: [] }
      }
      return { id: target, conflicts: await this.rebase(target) }
    },

    /** Restores a persisted workspace, if any. */
    restore(): boolean {
      try {
        const raw = localStorage.getItem(storageKey)
        if (!raw) return false
        // Older workspaces persisted datasources in an older shape; ignore them.
        const { datasources: _datasources, ...saved } = JSON.parse(raw) as Partial<State>
        this.$patch((state) => Object.assign(state, saved))
        // Workspaces saved before basePublished existed tracked their base.
        this.basePublished ??= this.baseId
        return Boolean(this.baseId)
      } catch {
        return false
      }
    },

    persist() {
      try {
        // Datasources come from the server on every load (see verifyBase).
        const { loading: _loading, datasources: _datasources, ...rest } = this.$state
        localStorage.setItem(storageKey, JSON.stringify(rest))
      } catch {
        // Storage may be unavailable (private mode); the workspace still works in memory.
      }
    },

    discard() {
      if (!this.baseline) return
      this.$patch((state) => Object.assign(state, JSON.parse(this.baseline) as Sections))
      this.conflicts = []
      this.persist()
    },

    /**
     * Restores one item to its baseline state. Reverting an addition removes
     * the item the same way deleting it would; references elsewhere that the
     * original change touched are separate changes and revert separately.
     */
    revert(change: Pick<Change, 'kind' | 'name'>) {
      if (!this.baseline) return
      const base = JSON.parse(this.baseline) as Sections
      // The baseline holds the newer revision's values: its side of conflicts.
      this.conflicts = this.conflicts.filter((c) => c.kind !== change.kind || c.name !== change.name)
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
      this.persist()
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
      this.persist()
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
      this.persist()
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
      this.persist()
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
      this.persist()
    },

    /**
     * Takes a plan's outcome (see previewPlan) into the workspace, like an
     * edit: the entities it adds are imported (references they make
     * ambiguous are qualified), the entities it changes take its fields, and
     * the roles and users it changes take its grants, which name entities by
     * ID and are written in their shortest unambiguous form.
     */
    applyProposal(p: Proposal) {
      const added = new Set(p.added)
      const entities = p.entities.map(toEntityInput)
      this.importEntities(entities.filter((e) => added.has(entityId(e))))
      for (const e of entities) {
        const current = added.has(entityId(e)) ? undefined : this.entity(entityId(e))
        if (current) current.fields = e.fields
      }
      const index = this.entityIndex
      const grants = (gs: GrantPartsFragment[]) => gs.map((g) => {
        const grant = toGrantInput(g)
        return index.get(g.entity) ? { ...grant, entity: index.shortName(g.entity) } : grant
      })
      for (const r of p.roles) {
        const role = this.role(r.name)
        if (role) role.grants = grants(r.grants)
        else this.roles.push({ name: r.name, description: r.description ?? undefined, grants: grants(r.grants) })
      }
      for (const u of p.users) {
        const user = this.user(u.name)
        if (user) user.grants = grants(u.grants)
      }
      this.persist()
    },

    upsertRole(r: RoleInput, previousName = r.name) {
      const i = this.roles.findIndex((x) => x.name === previousName)
      if (i >= 0) this.roles.splice(i, 1, r)
      else this.roles.push(r)
      if (previousName !== r.name) {
        for (const u of this.users) u.roles = (u.roles ?? []).map((x) => (x === previousName ? r.name : x))
      }
      this.persist()
    },

    /** Removes a role and takes it away from every user. */
    removeRole(name: string) {
      this.roles = this.roles.filter((r) => r.name !== name)
      for (const u of this.users) u.roles = (u.roles ?? []).filter((x) => x !== name)
      this.persist()
    },

    /** Sets which users hold a role. */
    setRoleMembers(role: string, members: string[]) {
      for (const u of this.users) {
        const has = (u.roles ?? []).includes(role)
        const want = members.includes(u.name)
        if (want && !has) u.roles = [...(u.roles ?? []), role]
        if (!want && has) u.roles = (u.roles ?? []).filter((x) => x !== role)
      }
      this.persist()
    },

    upsertUser(u: UserInput, previousName = u.name) {
      const i = this.users.findIndex((x) => x.name === previousName)
      if (i >= 0) this.users.splice(i, 1, u)
      else this.users.push(u)
      this.persist()
    },

    removeUser(name: string) {
      this.users = this.users.filter((u) => u.name !== name)
      this.persist()
    },

    setSettings(settings: Record<string, unknown>) {
      this.settings = settings
      this.persist()
    },
  },
})
