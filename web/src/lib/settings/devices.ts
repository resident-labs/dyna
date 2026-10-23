// DevicesPane helpers: one labelled chip per scope, and a device name that
// leaves the engine's session stamp out of the row.
import type { Device } from '../api/settings.ts'

// ScopeChip is one granted scope as the owner reads it: a short label and a
// sentence about what that scope lets a device do.
export interface ScopeChip {
  name: string
  label: string
  about: string
}

// SCOPES is every scope, in the order the pane lists them.
export const SCOPES: readonly ScopeChip[] = [
  { name: 'read', label: 'Read', about: 'See rooms, sessions and files.' },
  { name: 'operate', label: 'Operate', about: 'Post, start turns and change goals.' },
  { name: 'files', label: 'Files', about: 'Open and change workspace files.' },
  { name: 'terminal', label: 'Terminal', about: 'Run shell commands on this computer.' },
  { name: 'computers', label: 'Computers', about: 'See and drive the desktops and browsers.' },
  { name: 'admin', label: 'Admin', about: 'Change settings, rules and devices.' },
]

const NO_ACCESS: ScopeChip = { name: 'none', label: 'No access', about: 'The device can do nothing until you give it a scope.' }

const known = new Map(SCOPES.map((chip) => [chip.name, chip]))

// chip labels one scope. An unknown name from a newer engine keeps its words,
// so the row never hides a grant the owner gave.
function chip(name: string): ScopeChip {
  const found = known.get(name)
  if (found) return found
  const words = name.replace(/[_-]+/g, ' ').trim()
  if (!words) return NO_ACCESS
  return { name, label: words.charAt(0).toUpperCase() + words.slice(1), about: '' }
}

// scopeChips turns a grant into one chip per scope. A grant from before scopes
// existed holds all of them but reports only its own words, so it shows every chip.
export function scopeChips(d: Pick<Device, 'scopes' | 'scopeLabel' | 'legacy'>): ScopeChip[] {
  if (d.legacy) return [...SCOPES]
  const names = d.scopes?.length ? d.scopes : words(d.scopeLabel)
  if (names.length === 0) return [NO_ACCESS]
  return names.map(chip)
}

// words splits a joined scope label such as "read, operate" back into names.
function words(label?: string): string[] {
  return (label ?? '')
    .split(/,|\band\b/)
    .map((part) => part.trim().toLowerCase())
    .filter((part) => known.has(part))
}

// STAMP matches the session stamp the engine adds to a browser device name,
// as in "dyna browser · 20261023-112744-931569b1".
const STAMP = /^(.*?)\s*·\s*(\d{8}-\d{6}-[0-9a-z]+)\s*$/i

// DeviceName is how one row names its device.
export interface DeviceName {
  // title is the row's name: the browser or the host, never the stamp.
  title: string
  // stamp is the session stamp, empty when the engine gave none.
  stamp: string
  // tooltip names the computer session the stamp belongs to.
  tooltip: string
}

const UNNAMED = 'Unnamed device'

// deviceName names a device by its browser and host. Several browsers share
// the name "dyna browser", so the host and the last-seen line separate them,
// and the stamp stays in the tooltip.
export function deviceName(d: Pick<Device, 'name' | 'device' | 'origin'>): DeviceName {
  const raw = (d.name ?? '').trim()
  const matched = STAMP.exec(raw)
  const prefix = matched?.[1] ?? ''
  const stamp = matched?.[2] ?? ''
  const base = (prefix || raw).replace(/[\s·]+$/, '').trim()
  const host = originHost(d.origin)
  const name = base || (d.device ?? '').trim()
  const parts = [name, host && !name.toLowerCase().includes(host.toLowerCase()) ? host : '']
  return {
    title: parts.filter(Boolean).join(' · ') || UNNAMED,
    stamp,
    tooltip: stamp ? `Computer session ${stamp}` : '',
  }
}

// originHost is the address line of a Connect sign-in, without its scheme.
export function originHost(origin?: string): string {
  try {
    return new URL(origin ?? '').host
  } catch {
    return origin ?? ''
  }
}
