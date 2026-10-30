import { SvelteMap } from 'svelte/reactivity'
import type { Owner } from '../api/settings.ts'

// The owner's display name and avatar emoji, from owner.name and owner.avatar
// in settings.json. Every view that shows the owner reads them here; the app
// loads them at start and the owner bar's editor saves them. A SvelteMap keeps
// the reads reactive in plain TypeScript helpers such as speaker().
const profile = new SvelteMap<keyof Owner, string>()

// ownerName is the owner's chosen name, or "you" until they choose one.
export const ownerName = () => profile.get('name') || 'you'
// ownerAvatar is the owner's chosen emoji, or '' for the name's initials.
export const ownerAvatar = () => profile.get('avatar') ?? ''

export function setOwnerProfile(owner: Owner) {
  profile.set('name', owner.name)
  profile.set('avatar', owner.avatar)
}
