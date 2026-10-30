// One state vocabulary for every shared status part. Each tone has a CSS set
// in tokens.css: --tone-<t> (dot, fill), -fg (text), -wash and -edge. Live is
// warning drawn with a pulse, for work that is running now.
export type Tone = 'success' | 'warning' | 'danger' | 'info' | 'neutral' | 'live'

// Pages still compute state as the older color words. toneOf reads one such
// word where a page hands it to a shared part; any other word is neutral.
const fromColor: Record<string, Tone> = { sage: 'success', gold: 'warning', coral: 'danger', iris: 'info' }

export function toneOf(color: string): Tone {
  return fromColor[color] ?? 'neutral'
}
