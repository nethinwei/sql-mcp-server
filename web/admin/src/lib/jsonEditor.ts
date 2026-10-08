import type { Extension } from '@codemirror/state'
import { EditorView, hoverTooltip } from '@codemirror/view'
import { HighlightStyle, syntaxHighlighting, syntaxTree } from '@codemirror/language'
import type { Completion, CompletionContext, CompletionResult } from '@codemirror/autocomplete'
import { linter, type Diagnostic } from '@codemirror/lint'
import type { SyntaxNode } from '@lezer/common'
import { tags } from '@lezer/highlight'
import { at, contextAt, resolve, validate, type Path, type Schema } from './jsonSchema'

// Schema-aware completion, hover help and checks for a JSON document whose
// root sits at `path` inside the schema `root`.

export interface SchemaSource {
  root: Schema | null
  path: Path
}
export type Translate = (key: string, params?: Record<string, unknown>) => string

/** One-line summary of a schema: type plus range or allowed values. */
export function summary(s: Schema | undefined, t: Translate): string {
  if (!s) return ''
  const type = s['x-format'] === 'duration' ? 'duration' : s.type
  const parts = [type ? t(`settings.types.${type}`) : '']
  if (s.enum) parts.push(t('settings.values', { values: s.enum.map((e) => JSON.stringify(e)).join(' | ') }))
  else if (s.minimum !== undefined && s.maximum !== undefined) parts.push(t('settings.range', { min: s.minimum, max: s.maximum }))
  else if (s.minimum !== undefined) parts.push(`≥ ${s.minimum}`)
  else if (s.maximum !== undefined) parts.push(`≤ ${s.maximum}`)
  if (s['x-minimum'] !== undefined && s['x-maximum'] !== undefined) {
    parts.push(t('settings.range', { min: s['x-minimum'], max: s['x-maximum'] }))
  }
  if (s.default !== undefined) parts.push(t('settings.default', { value: JSON.stringify(s.default) }))
  if (s['x-restart']) parts.push(t('settings.restart'))
  return parts.filter(Boolean).join(' · ')
}

/** Text inserted for a new value of s and the selection inside it. */
function placeholder(s: Schema | undefined): { text: string; select: [number, number] } {
  if (s?.enum?.length) {
    const text = JSON.stringify(s.enum[0])
    return { text, select: typeof s.enum[0] === 'string' ? [1, text.length - 1] : [0, text.length] }
  }
  switch (s?.type) {
    case 'string': return { text: '""', select: [1, 1] }
    case 'boolean': return { text: 'false', select: [0, 5] }
    case 'integer': case 'number': return { text: String(s.minimum ?? 0), select: [0, String(s.minimum ?? 0).length] }
    case 'object': return { text: '{}', select: [1, 1] }
    case 'array': return { text: '[]', select: [1, 1] }
  }
  return { text: 'null', select: [0, 4] }
}

function info(s: Schema | undefined, t: Translate): Completion['info'] {
  if (!s?.description) return undefined
  return () => {
    const dom = document.createElement('div')
    dom.className = 'cm-schema-info'
    dom.textContent = s.description ?? ''
    const meta = summary(s, t)
    if (meta) {
      const m = document.createElement('div')
      m.className = 'cm-schema-meta'
      m.textContent = meta
      dom.appendChild(m)
    }
    return dom
  }
}

export function schemaCompletions(source: () => SchemaSource, t: Translate) {
  return (ctx: CompletionContext): CompletionResult | null => {
    const { root, path } = source()
    if (!root) return null
    const text = ctx.state.doc.toString()
    const c = contextAt(text, ctx.pos)
    if (!ctx.explicit && !c.prefix && !/[{,:[\s"]/.test(text[ctx.pos - 1] ?? '')) return null
    const quoted = text[c.from] === '"'
    const to = quoted && text[ctx.pos] === '"' ? ctx.pos + 1 : ctx.pos
    const insert = (view: EditorView, from: number, value: string, select: [number, number]) =>
      view.dispatch({
        changes: { from, to, insert: value },
        selection: { anchor: from + select[0], head: from + select[1] },
        userEvent: 'input.complete',
      })
    if (c.expect === 'key') {
      const s = at(root, root, [...path, ...c.container])
      if (!s?.properties) return null
      const present = presentKeys(ctx.state.sliceDoc(0), syntaxTree(ctx.state).resolveInner(c.from, -1), c.from)
      const options = Object.entries(s.properties).filter(([k]) => !present.has(k)).map(([k, v]): Completion => {
        const r = resolve(root, v)
        return {
          label: k, type: 'property', detail: summary(r, t), info: info(r, t),
          apply: (view) => {
            const p = placeholder(r)
            const head = `"${k}": `
            // Another property follows: separate it with a comma.
            const comma = /^\s*"/.test(text.slice(to)) ? ',' : ''
            insert(view, c.from, head + p.text + comma, [head.length + p.select[0], head.length + p.select[1]])
          },
        }
      })
      return { from: quoted ? c.from + 1 : c.from, options, validFor: /^[\w.-]*$/ }
    }
    const s = at(root, root, [...path, ...c.container, ...(c.key === undefined ? [] : [c.key])])
    const values = s?.enum ?? s?.examples ?? (s?.type === 'boolean' ? [true, false] : [])
    if (!values.length) return null
    const options = values.map((v): Completion => {
      const literal = JSON.stringify(v)
      return { label: literal, type: 'enum', apply: (view) => insert(view, c.from, literal, [literal.length, literal.length]) }
    })
    return { from: c.from, options, validFor: /^"?[\w.-]*"?$/ }
  }
}

/**
 * Property names already in the object around pos, read from the syntax tree
 * so it works while the document is incomplete. The name being typed at pos
 * does not count.
 */
function presentKeys(text: string, inner: SyntaxNode, pos: number): Set<string> {
  let obj: SyntaxNode | null = inner
  while (obj && obj.name !== 'Object') obj = obj.parent
  const out = new Set<string>()
  for (let p = obj?.firstChild; p; p = p.nextSibling) {
    const name = p.name === 'Property' ? p.firstChild : p.name === 'PropertyName' ? p : null
    if (name?.name !== 'PropertyName' || (name.from <= pos && pos <= name.to)) continue
    try {
      out.add(JSON.parse(text.slice(name.from, name.to)) as string)
    } catch {
      // An unterminated name is the one being typed.
    }
  }
  return out
}

/** The node for path below the document's top value; onKey picks the property name. */
function nodeAt(top: SyntaxNode | null, text: string, path: Path, onKey: boolean): SyntaxNode | null {
  let node = top
  for (let i = 0; node && i < path.length; i++) {
    const seg = path[i]
    const last = i === path.length - 1
    if (node.name === 'Object' && typeof seg === 'string') {
      let found: SyntaxNode | null = null
      for (let p = node.firstChild; p; p = p.nextSibling) {
        const name = p.name === 'Property' ? p.firstChild : null
        if (name?.name === 'PropertyName' && JSON.parse(text.slice(name.from, name.to)) === seg) {
          found = last && onKey ? name : p.lastChild
        }
      }
      node = found
    } else if (node.name === 'Array' && typeof seg === 'number') {
      const items: SyntaxNode[] = []
      for (let p = node.firstChild; p; p = p.nextSibling) if (!['[', ']', ','].includes(p.name)) items.push(p)
      node = items[seg] ?? null
    } else return null
  }
  return node
}

function checks(source: () => SchemaSource, t: Translate) {
  return linter((view) => {
    const text = view.state.doc.toString()
    const tree = syntaxTree(view.state)
    const out: Diagnostic[] = []
    tree.iterate({
      enter: (n) => {
        if (n.type.isError) out.push({ from: n.from, to: Math.max(n.to, n.from + 1), severity: 'error', message: t('settings.lint.syntax') })
      },
    })
    if (out.length) return out
    let value: unknown
    try {
      value = JSON.parse(text)
    } catch (e) {
      return [{ from: 0, to: Math.min(1, text.length), severity: 'error', message: e instanceof Error ? e.message : String(e) }]
    }
    const { root, path } = source()
    if (!root) return out
    const top = tree.topNode.firstChild
    for (const p of validate(root, at(root, root, path), value)) {
      const node = nodeAt(top, text, p.path, Boolean(p.onKey)) ?? top
      out.push({
        from: node?.from ?? 0, to: node?.to ?? 0, severity: p.severity,
        message: t(`settings.lint.${p.code}`,
          p.code === 'type' ? { type: t(`settings.types.${String(p.params.type)}`) } : p.params),
      })
    }
    return out
  }, { delay: 300 })
}

function hover(source: () => SchemaSource, t: Translate) {
  return hoverTooltip((view, pos, side) => {
    const node = syntaxTree(view.state).resolveInner(pos, side)
    if (node.name !== 'PropertyName') return null
    const { root, path } = source()
    if (!root) return null
    const text = view.state.doc.toString()
    const key = JSON.parse(text.slice(node.from, node.to)) as string
    const s = at(root, root, [...path, ...contextAt(text, node.from).container, key])
    if (!s) return null
    return {
      pos: node.from, end: node.to, above: true,
      create: () => {
        const dom = document.createElement('div')
        dom.className = 'cm-schema-hover'
        const head = document.createElement('div')
        head.className = 'cm-schema-head'
        head.textContent = key
        const meta = document.createElement('span')
        meta.className = 'cm-schema-meta'
        meta.textContent = summary(s, t)
        head.appendChild(meta)
        dom.appendChild(head)
        if (s.description) {
          const d = document.createElement('div')
          d.textContent = s.description
          dom.appendChild(d)
        }
        return { dom }
      },
    }
  })
}

export interface Palette {
  dark: boolean
  text: string
  muted: string
  background: string
  border: string
  primary: string
  popover: string
  selection: string
  key: string
  string: string
  number: string
  literal: string
}

export function look(p: Palette): Extension {
  return [
    EditorView.theme({
      '&': { color: p.text, backgroundColor: p.background, fontSize: '12.5px', border: `1px solid ${p.border}`, borderRadius: '6px' },
      '&.cm-focused': { outline: 'none', borderColor: p.primary },
      '.cm-content': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', caretColor: p.primary },
      '.cm-gutters': { backgroundColor: 'transparent', color: p.muted, border: 'none' },
      '.cm-activeLine, .cm-activeLineGutter': { backgroundColor: p.dark ? 'rgba(255,255,255,.04)' : 'rgba(0,0,0,.03)' },
      '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection': { backgroundColor: p.selection },
      '.cm-tooltip': { backgroundColor: p.popover, color: p.text, border: `1px solid ${p.border}`, borderRadius: '6px' },
      '.cm-tooltip-autocomplete ul li[aria-selected]': { backgroundColor: p.selection, color: p.text },
      '.cm-completionDetail': { color: p.muted, marginLeft: '8px', fontStyle: 'normal' },
      '.cm-schema-hover, .cm-schema-info': { padding: '6px 10px', maxWidth: '420px', fontSize: '12.5px', lineHeight: '1.5' },
      '.cm-schema-head': { fontFamily: 'ui-monospace, monospace', fontWeight: '600', marginBottom: '2px' },
      '.cm-schema-meta': { color: p.muted, fontWeight: 'normal', marginLeft: '8px', fontSize: '12px' },
      '.cm-schema-info .cm-schema-meta': { marginLeft: '0', marginTop: '4px' },
      '.cm-diagnostic': { padding: '4px 8px' },
    }, { dark: p.dark }),
    syntaxHighlighting(HighlightStyle.define([
      { tag: tags.propertyName, color: p.key },
      { tag: tags.string, color: p.string },
      { tag: tags.number, color: p.number },
      { tag: [tags.bool, tags.null], color: p.literal },
      { tag: tags.invalid, color: '#e5484d' },
    ])),
  ]
}

/** Hover help and checks against the schema from source; completion goes
 * through autocompletion({ override: [schemaCompletions(...)] }). */
export function schemaSupport(source: () => SchemaSource, t: Translate): Extension {
  return [hover(source, t), checks(source, t)]
}
