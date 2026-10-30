// Engine health and system load types come from Go.
export type * from './system.gen.ts'
export type { Health as WriteHealth } from './system-state.gen.ts'
export type { Stats as EventStats } from './system-hub.gen.ts'
export type { Split as LoadSplit, Memory as LoadMemory } from './system.gen.ts'
