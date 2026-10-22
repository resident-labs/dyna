import type { SettingItem } from '../api/settings.ts'

// Match the Go formatter: binary units with at most two exact decimals, or exact bytes.
export function formatBytes(value: string | number | bigint): string {
  const n = BigInt(value)
  const units = ['bytes', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB', 'EiB']
  for (let i = units.length - 1; i > 0; i--) {
    const scale = 1n << BigInt(10 * i)
    if (n < scale || n % (scale / 4n) !== 0n) continue
    const exact = n * 100n / scale
    const whole = exact / 100n
    const fraction = String(exact % 100n).padStart(2, '0').replace(/0+$/, '')
    return `${whole}${fraction ? `.${fraction}` : ''} ${units[i]}`
  }
  return `${n.toLocaleString('en-US')} bytes`
}

export function settingDisplay(item: Pick<SettingItem, 'display' | 'kind' | 'value'>): string {
  if (item.display) return item.display
  if (item.kind === 'bytes') return formatBytes(BigInt(String(item.value)))
  if (item.kind === 'duration') return String(item.value).replace(/m0s$/, 'm').replace(/h0m/g, 'h')
  return String(item.value)
}
