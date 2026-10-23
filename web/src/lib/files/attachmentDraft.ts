import { writable, type Readable } from 'svelte/store'
import { stage, upload, release, pastedFiles, MAX_FILES } from './attach.ts'

import type { DraftItem, PasteEvent } from './attach.ts'
export type { DraftItem, PasteEvent } from './attach.ts'

export interface DraftState {
  items: DraftItem[]
  ready: DraftItem[]
  uploading: boolean
  failed: boolean
  atts: { id: string; name: string }[]
}

export interface AttachmentDraft extends Readable<DraftState> {
  addFiles(files: ArrayLike<File> | null | undefined): void
  remove(item: DraftItem): void
  retry(item: DraftItem): void
  paste(event: PasteEvent): Promise<void>
  clear(): void
  destroy(): void
}

export interface AttachmentDraftOptions {
  room: () => string | undefined
  error: (message: string) => void
  focus: () => void
  disabled?: () => boolean
  noun?: string
}

function state(items: DraftItem[]): DraftState {
  const ready = items.filter((it) => it.status === 'ready')
  return {
    items, ready,
    uploading: items.some((it) => it.status === 'uploading'),
    failed: items.some((it) => it.status === 'failed'),
    atts: ready.map((it) => {
      if (!it.att) throw new TypeError(`ready attachment ${it.key} has no att`)
      return { id: it.att.id, name: it.name }
    }),
  }
}

export function attachmentDraft({ room, error, focus, disabled = () => false, noun = 'message' }: AttachmentDraftOptions): AttachmentDraft {
  let items: DraftItem[] = [], alive = true
  const store = writable(state(items))
  const set = (value: DraftItem[]) => { items = value; store.set(state(items)) }
  const refresh = (item: DraftItem) => { if (alive) set(items.map((it) => it.key === item.key ? { ...item } : it)) }
  function addFiles(files: ArrayLike<File> | null | undefined) {
    if (!alive || !room() || disabled() || !files?.length) return
    const take = Array.from(files).slice(0, Math.max(0, MAX_FILES - items.length))
    if (take.length < files.length) error(`A ${noun} can carry at most ${MAX_FILES} files.`)
    set([...items, ...stage(room(), take, refresh)])
    focus()
  }
  function clear() { release(items); set([]) }
  return {
    subscribe: store.subscribe,
    addFiles,
    remove(item) { release([item]); set(items.filter((it) => it.key !== item.key)) },
    retry(item) { if (alive && !disabled()) upload(room(), item, refresh) },
    async paste(event) {
      const hasText = !!event.clipboardData?.getData('text/plain')
      if (hasText) return
      event.preventDefault()
      const id = room()
      const files = await pastedFiles(event)
      if (id === room()) addFiles(files)
    },
    clear,
    destroy() { alive = false; clear() },
  }
}
