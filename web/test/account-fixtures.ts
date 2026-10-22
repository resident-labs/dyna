// Whole account and catalog objects for tests, each with every field its type requires. A test names
// only the fields it cares about.
import type { CatalogModel, Effort, UsageAccount } from '../src/lib/api/account.ts'

export const usageAccount = (fields: Partial<UsageAccount> & Pick<UsageAccount, 'id' | 'harness'>): UsageAccount => ({
  quotaBlocked: false, label: '', providerId: '', active: false, status: 'ready', source: '', observedAt: '', stale: false, profiles: [], windows: [], needsLogin: false, ...fields,
})

export const catalogModel = (id: string, fields: Partial<CatalogModel> = {}): CatalogModel => ({ id, label: id, category: 'other', source: '', limits: null, ...fields })

export const effortOf = (id: string, fields: Partial<Effort> = {}): Effort => ({ id, args: null, ...fields })
