// These unions remain handwritten because tygo does not recognize the Go
// NoticeKind constants' Kind prefix or the Severity constants' different name.
export type NoticeKind = 'disk' | 'provider' | 'sync' | 'tunnel'
export type NoticeSeverity = 'warn' | 'error'

export type * from './settings.gen.ts'
export type { Item as SettingItem, Run as HookRun, Summary as HookSummary } from './settings.gen.ts'
