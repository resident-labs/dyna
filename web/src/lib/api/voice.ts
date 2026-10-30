// Voice response types come from Go. The page types below describe validated socket variants.
export type * from './voice.gen.ts'

// VoicePageEvent is realtime.PageEvent: one message from the engine to the
// page. Go sends one struct and omits unused fields; each member lists the
// fields its type sets.
export type VoicePageEvent =
  | { type: 'resume_cursor'; cursor: string }
  | { type: 'state'; state: string } // listening, thinking, speaking, reconnecting
  | { type: 'audio'; audio: string; id?: string }
  | { type: 'audio_done'; id?: string }
  | { type: 'clear'; id?: string }
  | { type: 'transcript'; text?: string; final?: boolean }
  | { type: 'unspoken'; post: string }
  | { type: 'tool'; id: string; name?: string; text?: string; state?: string } // state: running, done or failed
  | { type: 'error'; message?: string }

// VoicePageMessage is realtime.PageMessage: one message from the page.
export type VoicePageMessage =
  | { type: 'audio'; audio: string }
  | { type: 'mute'; muted: boolean }
  | { type: 'cancel' }
  | { type: 'playback'; id: string; state: string; played_ms: number } // state: started or ended
  | { type: 'hangup' }
