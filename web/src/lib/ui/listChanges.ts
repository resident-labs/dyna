import type { ListChange } from '../api/event.ts'
import { reconcileRows } from './reloadGate.ts'

type RowKey<T> = string | ((row: T) => unknown)
// A list change whose item is already known to be a row of the list.
export type RowChange<T> = Omit<ListChange, 'item'> & { item?: T }

export function hasItemChange<T extends object>(change: RowChange<T> | null | undefined): change is RowChange<T> & ({ op: 'delete' } | { op: 'upsert'; item: T }) {
  return change?.op === 'delete' || (change?.op === 'upsert' && !!change.item)
}

export function patchListRows<T extends object>(rows: T[], change: RowChange<T> | null | undefined, key: RowKey<T> = 'id'): T[] {
  if (!hasItemChange(change)) return rows
  const identify = typeof key === 'function' ? key : (row: T): unknown => Reflect.get(row, key)
  if (change.op === 'delete') return reconcileRows(rows, rows.filter((row) => identify(row) !== change.id), key)
  if (identify(change.item) !== change.id) return rows
  const found = rows.some((row) => identify(row) === change.id)
  if (!found && change.inHead === false) return rows
  const incoming = found ? rows.map((row) => identify(row) === change.id ? change.item : row) : [...rows, change.item]
  return reconcileRows(rows, incoming, key)
}
