import { useEffect, useRef } from 'react'
import {
  autocompletion,
  type CompletionContext,
  type CompletionResult
} from '@codemirror/autocomplete'
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands'
import { markdown } from '@codemirror/lang-markdown'
import { defaultHighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { Compartment, EditorState } from '@codemirror/state'
import {
  Decoration,
  EditorView,
  ViewPlugin,
  drawSelection,
  keymap,
  type DecorationSet,
  type ViewUpdate
} from '@codemirror/view'

import { useT, type Translations } from '../../i18n'
import {
  fetchReferenceCandidates,
  referenceKindLabel,
  referenceTrigger,
  replaceReference,
  rosLinkMatches,
  type ReferenceCandidate
} from './reference'
import styles from './notes.module.css'

export interface MarkdownEditorProps {
  readonly value: string
  readonly projectId: string
  readonly readOnly?: boolean
  readonly onChange: (markdown: string) => void
  /** Ctrl/Cmd+S: bypass the debounce and save now. */
  readonly onSave: () => void
  readonly onOpenReference: (href: string) => void
}

const REFERENCE_CLASS = 'cm-ros-link'

/**
 * referenceCompletionSource is the `[[` source, exported so its behaviour is
 * testable without a live editor. It returns null when the trigger is absent.
 */
export function referenceCompletionSource(
  projectId: string,
  t: Translations
): (context: CompletionContext) => Promise<CompletionResult | null> {
  return async (context) => {
    const trigger = referenceTrigger(context.state, context.pos)
    if (trigger === null) return null
    let candidates: ReferenceCandidate[] = []
    try {
      candidates = await fetchReferenceCandidates(projectId, trigger.query)
    } catch {
      return null
    }
    if (candidates.length === 0) return null
    return {
      from: trigger.from,
      options: candidates.map((candidate) => ({
        label: candidate.title === '' ? t('pages.notes.untitled') : candidate.title,
        detail: t(referenceKindLabel(candidate.type)),
        apply: (view, _completion, from, to) => {
          const change = replaceReference(view.state, from, to, candidate)
          view.dispatch({
            changes: change.changes,
            selection: change.selection,
            scrollIntoView: true
          })
        }
      })),
      // Only the query text makes the list stale; a new character re-fetches.
      validFor: /^\[\[[^[\]\n]*$/u
    }
  }
}

function referenceDecorations(view: EditorView): DecorationSet {
  const marks = rosLinkMatches(view.state.doc.toString()).map((match) =>
    Decoration.mark({ class: REFERENCE_CLASS, attributes: { 'data-ros-href': match.href } }).range(
      match.from,
      match.to
    )
  )
  return Decoration.set(marks, true)
}

const referencePlugin = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet

    constructor(view: EditorView) {
      this.decorations = referenceDecorations(view)
    }

    update(update: ViewUpdate): void {
      if (update.docChanged || update.viewportChanged) {
        this.decorations = referenceDecorations(update.view)
      }
    }
  },
  { decorations: (plugin) => plugin.decorations }
)

/**
 * MarkdownEditor is the CodeMirror 6 surface: Markdown highlighting, history,
 * the `[[` picker, clickable `ros://` spans and the immediate-save chord.
 */
export function MarkdownEditor({
  value,
  projectId,
  readOnly = false,
  onChange,
  onSave,
  onOpenReference
}: MarkdownEditorProps): JSX.Element {
  const t = useT()
  const hostRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)
  const onChangeRef = useRef(onChange)
  const onSaveRef = useRef(onSave)
  const onOpenReferenceRef = useRef(onOpenReference)
  const suppressRef = useRef(false)
  onChangeRef.current = onChange
  onSaveRef.current = onSave
  onOpenReferenceRef.current = onOpenReference

  const readOnlyRef = useRef(readOnly)
  readOnlyRef.current = readOnly
  const readonlyCompartment = useRef(new Compartment())
  const editableCompartment = useRef(new Compartment())

  useEffect(() => {
    const host = hostRef.current
    if (host === null) return

    const source = referenceCompletionSource(projectId, t)
    const view = new EditorView({
      parent: host,
      state: EditorState.create({
        doc: value,
        extensions: [
          markdown(),
          history(),
          drawSelection(),
          EditorView.lineWrapping,
          syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
          autocompletion({ override: [source], activateOnTyping: true }),
          keymap.of([
            {
              key: 'Mod-s',
              preventDefault: true,
              run: () => {
                onSaveRef.current()
                return true
              }
            },
            ...defaultKeymap,
            ...historyKeymap
          ]),
          readonlyCompartment.current.of(EditorState.readOnly.of(readOnlyRef.current)),
          editableCompartment.current.of(EditorView.editable.of(!readOnlyRef.current)),
          referencePlugin,
          EditorView.domEventHandlers({
            mousedown: (event) => {
              const target = event.target
              if (!(target instanceof HTMLElement)) return false
              const element = target.closest<HTMLElement>('[data-ros-href]')
              if (element === null) return false
              const href = element.dataset.rosHref
              if (href === undefined) return false
              event.preventDefault()
              onOpenReferenceRef.current(href)
              return true
            }
          }),
          EditorView.updateListener.of((update) => {
            if (update.docChanged && !suppressRef.current) {
              onChangeRef.current(update.state.doc.toString())
            }
          }),
          EditorView.theme({
            '&': { height: '100%', fontSize: 'var(--text-body-size)' },
            '.cm-scroller': {
              fontFamily: 'var(--font-mono)',
              lineHeight: 'var(--line-height-body)',
              overflow: 'auto'
            },
            '.cm-content': { padding: 'var(--space-3)', caretColor: 'var(--text-primary)' },
            '.cm-ros-link': { color: 'var(--accent-default)', textDecoration: 'underline' },
            '&.cm-focused': { outline: 'none' },
            '.cm-gutters': { backgroundColor: 'transparent', border: 'none' }
          })
        ]
      })
    })
    viewRef.current = view
    return () => {
      view.destroy()
      viewRef.current = null
    }
    // The editor is created once per note route; `value` changes are applied
    // below instead of rebuilding the view and losing history/selection.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId])

  useEffect(() => {
    const view = viewRef.current
    if (view === null) return
    const current = view.state.doc.toString()
    if (current === value) return
    suppressRef.current = true
    view.dispatch({
      changes: { from: 0, to: current.length, insert: value },
      selection: { anchor: Math.min(view.state.selection.main.anchor, value.length) }
    })
    suppressRef.current = false
  }, [value])

  useEffect(() => {
    const view = viewRef.current
    if (view === null) return
    view.dispatch({
      effects: [
        editableCompartment.current.reconfigure(EditorView.editable.of(!readOnly)),
        readonlyCompartment.current.reconfigure(EditorState.readOnly.of(readOnly))
      ]
    })
  }, [readOnly])

  return <div ref={hostRef} className={styles.editor} data-testid="markdown-editor" />
}