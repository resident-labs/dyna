import { reconcileRows } from './reloadGate.js'

export function hasItemChange(change) {
  return change?.op === 'delete' || (change?.op === 'upsert' && !!change.item)
}

export function patchListRows(rows, change, key = 'id') {
  if (!hasItemChange(change)) return rows
  const identify = typeof key === 'function' ? key : (row) => row[key]
  if (change.op === 'delete') return reconcileRows(rows, rows.filter((row) => identify(row) !== change.id), key)
  if (identify(change.item) !== change.id) return rows
  const found = rows.some((row) => identify(row) === change.id)
  if (!found && change.inHead === false) return rows
  const incoming = found ? rows.map((row) => identify(row) === change.id ? change.item : row) : [...rows, change.item]
  return reconcileRows(rows, incoming, key)
}
