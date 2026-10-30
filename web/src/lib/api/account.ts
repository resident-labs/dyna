// Account wire types come from Go. Client-only display fields are not part of
// the wire contract, so UsageWindow keeps those optional fields here.
import type { Kind, Provider as KnownProvider, Window } from './account.gen.ts'

export type * from './account.gen.ts'
export type {
  Renewal as AccountRenewal,
  Limit as ModelLimit,
  Model as CatalogModel,
} from './account.gen.ts'
export type UsageWindow = Window & {
  amountLabel?: string
  remainingPercent?: number
}

// Empty is the Go zero value, which tygo cannot infer from named constants.
export type Provider = KnownProvider | ''
export type AccountKind = Kind | ''
