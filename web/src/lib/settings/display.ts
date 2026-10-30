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

// A choice list longer than this shows as a select; a shorter one as a segmented control.
const SEGMENT_LIMIT = 4

// usesSelect says a choice setting shows as a select, so a long list fits its row.
export function usesSelect(item: Pick<SettingItem, 'kind' | 'options'>): boolean {
  return item.kind === 'select' || (item.kind === 'enum' && (item.options?.length ?? 0) > SEGMENT_LIMIT)
}

// choiceNote is the line under the control for the chosen option, or '' when it has none.
export function choiceNote(item: Pick<SettingItem, 'options' | 'optionNotes' | 'value'>): string {
  const index = item.options?.indexOf(String(item.value)) ?? -1
  return index < 0 ? '' : (item.optionNotes?.[index] ?? '')
}
