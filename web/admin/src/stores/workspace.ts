import { defineStore } from 'pinia'
import { run } from '@/api/client'
import { PublishedQuery, WorkspaceQuery } from '@/api/ops'
import type {
  Action,
  WorkspaceQuery as WorkspaceResult,
  DraftInput,
  EntityInput,
  EntityPartsFragment,
  GrantInput,
  GrantPartsFragment,
  RoleInput,
  UserInput,
} from '@/gql/graphql'

export interface Datasource {
  name: string
  driver: string
  dsn: string
}

/** What the workspace edits; also the shape persisted to localStorage. */
interface Sections {
  entities: EntityInput[]
  roles: RoleInput[]
  users: UserInput[]
  settings: Record<string, unknown>
}

export interface Change {
  kind: 'entity' | 'role' | 'user' | 'settings'
  name: string
  type: 'added' | 'removed' | 'modified'
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
    params: [...e.params],
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

export function toGrantInput(g: GrantPartsFragment): GrantInput {
  return {
    entity: g.entity,
    actions: [...g.actions],
    fieldsRestricted: g.fieldsRestricted,
    readFields: [...g.readFields],
    writeFields: [...g.writeFields],
    rows: g.rows ?? undefined,
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

type Item = Record<string, unknown>

/**
 * Three-way merge of one item by top-level property: local changes apply on
 * top of next; a property both sides changed differently is a conflict and
 * keeps the local value. Additions and removals take the local side.
 */
export function merge3(base: unknown, local: unknown, next: unknown): { value: unknown; conflict: boolean } {
  if (base === undefined || local === undefined || next === undefined) {
    const theirs = canonical(next)
    return { value: local, conflict: theirs !== canonical(base) && theirs !== canonical(local) }
  }
  const [b, l, n] = [base as Item, local as Item, next as Item]
  const value: Item = { ...n }
  let conflict = false
  for (const key of new Set([...Object.keys(b), ...Object.keys(l)])) {
    if (canonical({ [key]: l[key] }) === canonical({ [key]: b[key] })) continue
    if (canonical({ [key]: n[key] }) !== canonical({ [key]: b[key] }) &&
      canonical({ [key]: n[key] }) !== canonical({ [key]: l[key] })) conflict = true
    if (l[key] === undefined) delete value[key]
    else value[key] = l[key]
  }
  return { value, conflict }
}

/** The item a change refers to in sections, or undefined when absent. */
function pick(s: Sections, c: Pick<Change, 'kind' | 'name'>): unknown {
  if (c.kind === 'settings') return s.settings
  const items: { name: string }[] = c.kind === 'entity' ? s.entities : c.kind === 'role' ? s.roles : s.users
  return items.find((x) => x.name === c.name)
}

function byName<T extends { name: string }>(items: T[]): Map<string, string> {
  return new Map(items.map((i) => [i.name, canonical(i)]))
}

function diffNamed<T extends { name: string }>(kind: Change['kind'], base: T[], now: T[]): Change[] {
  const before = byName(base)
  const after = byName(now)
  const out: Change[] = []
  for (const [name, json] of after) {
    if (!before.has(name)) out.push({ kind, name, type: 'added' })
    else if (before.get(name) !== json) out.push({ kind, name, type: 'modified' })
  }
  for (const name of before.keys()) if (!after.has(name)) out.push({ kind, name, type: 'removed' })
  return out
}

function dropGrantsOn(grants: GrantInput[] | null | undefined, entity: string): GrantInput[] {
  return (grants ?? []).filter((g) => g.entity !== entity)
}

export const useWorkspace = defineStore('workspace', {
  state: (): State => ({
    baseId: null,
    basePublished: null,
    baseHash: '',
    datasources: [],
    baseTokens: {},
    baseline: '',
    loading: false,
    entities: [],
    roles: [],
    users: [],
    settings: {},
  }),

  getters: {
    changes(state): Change[] {
      if (!state.baseline) return []
      const base = JSON.parse(state.baseline) as Sections
      const out = [
        ...diffNamed('entity', base.entities, state.entities),
        ...diffNamed('role', base.roles, state.roles),
        ...diffNamed('user', base.users, state.users),
      ]
      if (JSON.stringify(base.settings) !== JSON.stringify(state.settings)) {
        out.push({ kind: 'settings', name: 'settings', type: 'modified' })
      }
      return out
    },
    dirty(): boolean {
      return this.changes.length > 0
    },
    draft(state): DraftInput {
      return {
        base: state.baseId ?? '',
        entities: state.entities,
        roles: state.roles,
        users: state.users,
        settings: state.settings,
      }
    },
    entity: (state) => (name: string) => state.entities.find((e) => e.name === name),
    role: (state) => (name: string) => state.roles.find((r) => r.name === name),
    user: (state) => (name: string) => state.users.find((u) => u.name === name),
    /** Roles and users holding a grant on an entity. */
    accessTo: (state) => (entity: string) => ({
      roles: state.roles.filter((r) => (r.grants ?? []).some((g) => g.entity === entity)).map((r) => r.name),
      users: state.users.filter((u) => (u.grants ?? []).some((g) => g.entity === entity)).map((u) => u.name),
    }),
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
     * changes with a three-way merge per item property (description, grants,
     * fields, ...). Returns the changes where id changed a property this
     * workspace changed differently: the local value wins, and reverting the
     * item takes id's version.
     */
    async rebase(id: string): Promise<Change[]> {
      this.loading = true
      try {
        return this.rebaseOnto(await run(WorkspaceQuery, { id }), id)
      } finally {
        this.loading = false
      }
    },

    rebaseOnto(data: WorkspaceResult, id: string): Change[] {
      const oldBase = JSON.parse(this.baseline) as Sections
      const local = JSON.parse(JSON.stringify(sections(this.$state))) as Sections
      const changes = this.changes
      this.apply(data, id)
      const newBase = JSON.parse(this.baseline) as Sections
      const conflicts: Change[] = []
      for (const c of changes) {
        const merged = merge3(pick(oldBase, c), pick(local, c), pick(newBase, c))
        if (merged.conflict) conflicts.push(c)
        const mine = merged.value as never
        if (c.kind === 'settings') this.settings = mine
        else if (c.type === 'removed') {
          if (c.kind === 'entity') this.removeEntity(c.name)
          else if (c.kind === 'role') this.removeRole(c.name)
          else this.removeUser(c.name)
        } else if (c.kind === 'entity') this.upsertEntity(mine)
        else if (c.kind === 'role') this.upsertRole(mine)
        else this.upsertUser(mine)
      }
      this.persist()
      return conflicts
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
      this.entities = cfg.entities.map(toEntityInput)
      this.roles = cfg.roles.map((r) => ({
        name: r.name,
        description: r.description ?? undefined,
        grants: r.grants.map(toGrantInput),
      }))
      this.users = cfg.users.map((u) => ({
        name: u.name,
        description: u.description ?? undefined,
        roles: [...u.roles],
        subject: u.subject ?? undefined,
        disabled: u.disabled,
        grants: u.grants.map(toGrantInput),
      }))
      this.settings = { ...(cfg.settings as Record<string, unknown>) }
      this.baseline = JSON.stringify(sections(this.$state))
      this.persist()
    },

    /** Loads the published revision; returns false when nothing is published. */
    async loadPublished(): Promise<boolean> {
      const data = await run(PublishedQuery, {})
      if (!data.published) return false
      await this.load(data.published.id)
      return true
    },

    /** Restores a persisted workspace, if any. */
    restore(): boolean {
      try {
        const raw = localStorage.getItem(storageKey)
        if (!raw) return false
        this.$patch((state) => Object.assign(state, JSON.parse(raw) as Partial<State>))
        // Workspaces saved before basePublished existed tracked their base.
        this.basePublished ??= this.baseId
        return Boolean(this.baseId)
      } catch {
        return false
      }
    },

    persist() {
      try {
        const { loading: _loading, ...rest } = this.$state
        localStorage.setItem(storageKey, JSON.stringify(rest))
      } catch {
        // Storage may be unavailable (private mode); the workspace still works in memory.
      }
    },

    discard() {
      if (!this.baseline) return
      this.$patch((state) => Object.assign(state, JSON.parse(this.baseline) as Sections))
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
      if (change.kind === 'settings') return this.setSettings(base.settings)
      const restore = <T extends { name: string }>(items: T[], baseItems: T[]) => {
        const original = baseItems.find((x) => x.name === change.name)
        const i = items.findIndex((x) => x.name === change.name)
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
     * and grants them to the named roles (created when missing). Returns how
     * many relationships were dropped.
     */
    importEntities(entities: EntityInput[], roles: string[] = [], actions: Action[] = []): number {
      const names = new Set([...this.entities.map((e) => e.name), ...entities.map((e) => e.name)])
      let dropped = 0
      for (const e of entities) {
        const relationships = (e.relationships ?? []).filter((r) => names.has(r.target))
        dropped += (e.relationships?.length ?? 0) - relationships.length
        this.upsertEntity({ ...e, relationships })
      }
      if (actions.length) {
        for (const name of roles) {
          const role = this.role(name) ?? { name, grants: [] }
          this.upsertRole({
            ...role,
            grants: [...(role.grants ?? []), ...entities.map((e) => ({ entity: e.name, actions: [...actions] }))],
          })
        }
      }
      return dropped
    },

    /**
     * Adds or replaces an entity. Renaming it (previousName differs) keeps
     * the table it reads (source defaults to the name) and moves every
     * reference along: grants of roles and users, and relationships of other
     * entities that target it.
     */
    upsertEntity(e: EntityInput, previousName = e.name) {
      if (previousName !== e.name && !e.source) e = { ...e, source: previousName }
      const i = this.entities.findIndex((x) => x.name === previousName)
      if (i >= 0) this.entities.splice(i, 1, e)
      else this.entities.push(e)
      if (previousName !== e.name) {
        const rename = (g: GrantInput) => (g.entity === previousName ? { ...g, entity: e.name } : g)
        for (const r of this.roles) r.grants = (r.grants ?? []).map(rename)
        for (const u of this.users) u.grants = (u.grants ?? []).map(rename)
        for (const other of this.entities) {
          if (other.relationships?.some((rel) => rel.target === previousName)) {
            other.relationships = other.relationships.map((rel) =>
              (rel.target === previousName ? { ...rel, target: e.name } : rel))
          }
        }
      }
      this.persist()
    },

    /** Removes an entity, every grant on it and every relationship to it. */
    removeEntity(name: string) {
      this.entities = this.entities.filter((e) => e.name !== name)
      for (const r of this.roles) r.grants = dropGrantsOn(r.grants, name)
      for (const u of this.users) u.grants = dropGrantsOn(u.grants, name)
      for (const other of this.entities) {
        if (other.relationships?.some((rel) => rel.target === name)) {
          other.relationships = other.relationships.filter((rel) => rel.target !== name)
        }
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
