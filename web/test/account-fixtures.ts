// Whole account and catalog objects for tests, each with every field its type requires. A test names
// only the fields it cares about.
import type { Account, CatalogModel, Effort } from '../src/lib/api/account.ts'

export const account = (fields: Partial<Account> & Pick<Account, 'id' | 'harness'>): Account => ({
  quotaBlocked: false, label: '', providerId: '', repos: null, active: false, status: 'ready', source: '', observedAt: '', stale: false, profiles: [], windows: [], needsLogin: false, ...fields,
})

export const catalogModel = (id: string, fields: Partial<CatalogModel> = {}): CatalogModel => ({ id, label: id, category: 'other', source: '', limits: null, ...fields })

export const effortOf = (id: string, fields: Partial<Effort> = {}): Effort => ({ id, args: null, ...fields })
