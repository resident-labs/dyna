const key = (item) => item.key ?? item.id

// The first list wins shared keys and sets their order. Later pages cannot
// replace newer rows that a current-page refresh already supplied.
function combine(first, second) {
  const rows = new Map(first.map((item) => [key(item), item]))
  for (const item of second) if (!rows.has(key(item))) rows.set(key(item), item)
  return [...rows.values()]
}

// A refresh replaces page 1 until the user loads history. After that, keep
// the loaded rows and their continuation cursor until navigation resets it.
export function mergeListPage(previous, incoming, append = false) {
  if (!previous) return { ...incoming, loadedOlder: append }
  if (append) return { ...incoming, loadedOlder: true, items: combine(previous.items, incoming.items) }
  if (previous.loadedOlder) return { ...previous, items: combine(incoming.items, previous.items) }
  return { ...incoming, loadedOlder: false }
}
