// Harness wire types come from Go.
export type * from './harness.gen.ts'
export type {
  NativeAccount as HarnessAccount,
  PrivateRuntimeSnapshot as HarnessSnapshot,
} from './harness.gen.ts'

// No Go response type exists for this legacy, unused public API shape.
import type { ProviderKey } from './harness.gen.ts'
export interface ProviderKeysResult {
  providers: ProviderKey[]
  choices?: ProviderKey[]
}
