import { formatBytes } from '../settings/display.ts'
import { api } from '../../api.js'

// Staging for composer attachments. Each file is saved with the room as
// soon as it is added, so the post only names bytes that are already stored.
export const MAX_FILES = 8

let seq = 0

function base64(file) {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => resolve(String(r.result).replace(/^data:[^,]*,/, ''))
    r.onerror = () => reject(r.error || new Error('could not read the file'))
    r.readAsDataURL(file)
  })
}

// fileName names pasted images that arrive without a useful name.
function fileName(file) {
  if (file.name && file.name !== 'image.png') return file.name
  const ext = (file.type.split('/')[1] || 'png').replace('jpeg', 'jpg')
  const d = new Date()
  const two = (n) => String(n).padStart(2, '0')
  return `pasted-${d.getFullYear()}${two(d.getMonth() + 1)}${two(d.getDate())}-${two(d.getHours())}${two(d.getMinutes())}${two(d.getSeconds())}.${ext}`
}

// stage returns staged items for files and starts their uploads. update is
// called with each item as its state changes.
export function stage(roomId, files, update) {
  return Array.from(files).map((file) => {
    const item = {
      key: `f${++seq}`,
      file,
      name: fileName(file),
      size: file.size,
      image: /^image\/(png|jpeg|gif|webp)$/.test(file.type),
      url: '',
      status: 'uploading',
      error: '',
      att: null,
    }
    if (item.image) item.url = URL.createObjectURL(file)
    upload(roomId, item, update)
    return item
  })
}

export async function upload(roomId, item, update) {
  item.status = 'uploading'
  item.error = ''
  update(item)
  try {
    const limits = await api.settings()
    const limit = limits.find((it) => it.key === 'content.maxFileBytes')?.value
    if (!Number.isSafeInteger(limit) || limit < 1) throw new Error('could not read the attachment size limit')
    if (item.size > limit) throw new Error(`the file is too large (maximum ${formatBytes(limit)})`)
    item.att = await api.attach(roomId, item.name, await base64(item.file))
    item.status = 'ready'
  } catch (e) {
    item.status = 'failed'
    item.error = String(e?.message || e)
  }
  update(item)
}

export function release(items) {
  for (const it of items) if (it.url) URL.revokeObjectURL(it.url)
}

export function human(n) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

export function extOf(name) {
  const m = /\.([A-Za-z0-9]{1,6})$/.exec(name || '')
  return m ? m[1].toUpperCase() : 'FILE'
}

// pastedFiles collects files from a paste event. WebKitGTK does not always
// fill clipboardData.files for a copied image, so it also tries the items
// and then the async Clipboard API. It resolves to [] when the clipboard
// holds no file.
export async function pastedFiles(e) {
  const dt = e.clipboardData
  let files = Array.from(dt?.files || [])
  if (!files.length) {
    for (const it of Array.from(dt?.items || [])) {
      if (it.kind === 'file') {
        const f = it.getAsFile()
        if (f) files.push(f)
      }
    }
  }
  if (files.length || (dt && dt.getData('text/plain'))) return files
  try {
    for (const item of await navigator.clipboard.read()) {
      const type = item.types.find((t) => /^image\/(png|jpeg|gif|webp)$/.test(t))
      if (type) {
        const blob = await item.getType(type)
        files.push(new File([blob], `image.${type.split('/')[1]}`, { type }))
      }
    }
  } catch {
    // No permission or no image: nothing to attach.
  }
  return files
}
