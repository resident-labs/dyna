export const LOAD_POLL_MS = 5000

// pollLoad reads once now and then every `every` ms, until the returned stop
// function runs. An answer older than one already shown is dropped, and so is
// any answer after stop. A failed read keeps the last value.
export function pollLoad(read, apply, every = LOAD_POLL_MS) {
  let sent = 0
  let shown = 0
  let alive = true
  const tick = () => {
    const n = ++sent
    read().then((v) => { if (alive && n > shown) { shown = n; apply(v) } }).catch(() => {})
  }
  tick()
  const timer = setInterval(tick, every)
  return () => { alive = false; clearInterval(timer) }
}

const GIB = 2 ** 30
const gib = (bytes) => `${(bytes / GIB).toFixed(1)} GiB`
const share = (part, whole) => (whole > 0 ? Math.min(100, (part / whole) * 100) : 0)

// loadView turns one SystemLoad answer into what the card draws. cpu is null
// until the engine has two samples. The split and top list exist only when
// the engine knows Dyna's share.
export function loadView(l) {
  const [one, five, fifteen] = l.load
  const m = l.memory
  const dyna = l.dynaKnown ? Math.min(m.dyna, m.used) : 0
  const mem = [
    ...(l.dynaKnown ? [{ key: 'dyna', label: 'Dyna', bytes: dyna }] : []),
    { key: 'other', label: 'Other', bytes: m.used - dyna },
    { key: 'free', label: 'Free', bytes: m.total - m.used },
  ].map((s) => ({ ...s, text: gib(s.bytes), pct: share(s.bytes, m.total) }))
  const split = l.split && [
    { key: 'engine', label: 'Engine', pct: l.split.engine },
    { key: 'agents', label: 'Agents', pct: l.split.agents },
    { key: 'computers', label: 'Computers', pct: l.split.computers },
    { key: 'other', label: 'Other', pct: l.split.other },
  ].map((s) => ({ ...s, text: `${s.pct.toFixed(0)}%` }))
  return {
    cpu: l.cpuPercent == null ? null : Math.round(l.cpuPercent),
    loadText: `load ${[one, five, fifteen].map((v) => v.toFixed(1)).join(' · ')} · ${l.cpus} CPUs`,
    hot: one > l.cpus,
    known: l.dynaKnown,
    split: split ?? [],
    mem,
    memText: `${gib(m.used)} of ${gib(m.total)}`,
    top: l.dynaKnown ? l.top.map((t) => ({ ...t, cores: `${t.cores.toFixed(1)} cores` })) : [],
  }
}
