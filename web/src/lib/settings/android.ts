// The Android call app: where a phone downloads it, and the link that pairs it in one tap.

// appLink is the APK download address on the web app's address.
export function appLink(origin: string): string {
  return `${origin.replace(/\/+$/, '')}/api/android/app.apk`
}

// pairLink opens the installed app and pairs it with the one-time code.
export function pairLink(origin: string, code: string): string {
  return `dyna://pair?url=${encodeURIComponent(origin.replace(/\/+$/, ''))}&code=${encodeURIComponent(code)}`
}

// FIRST_PAIR_LINK is the first app version that opens a dyna://pair link. An older app has no
// such link handler, so the wizard offers the Android app only when the engine serves this
// version or a newer one.
export const FIRST_PAIR_LINK = 2

// appVersion is the versionCode of the app this engine serves, or 0 when it has none. A missing
// build answers 404, and a page with no engine behind it (or a failed call) offers nothing.
export async function appVersion(fetcher: typeof fetch = fetch): Promise<number> {
  try {
    const r = await fetcher('/api/android/version', { cache: 'no-store' })
    if (!r.ok) return 0
    const body: unknown = await r.json()
    const code = typeof body === 'object' && body !== null && 'versionCode' in body ? body.versionCode : 0
    return typeof code === 'number' ? code : 0
  } catch {
    return 0
  }
}

// hasApp says whether to offer the Android app: the engine serves a build that can pair by link.
export async function hasApp(fetcher: typeof fetch = fetch): Promise<boolean> {
  return (await appVersion(fetcher)) >= FIRST_PAIR_LINK
}
