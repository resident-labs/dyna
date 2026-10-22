<script lang="ts">
  import type { Snippet } from 'svelte'
  import { api } from '../../api.js'
  import type { ChangeDetail as Detail } from '../api/change.ts'
  import { errorText } from '../errorText.ts'
  import type { Row } from './prlist.ts'

  interface Props {
    row?: Row | null
    roomId?: string
    detail?: Detail | null
    children: Snippet<[Detail | null, string, boolean, () => void]>
  }
  let { row, roomId = '', detail = $bindable(null), children }: Props = $props()
  let error = $state('')
  let loading = $state(false)
  let revision = $state(0)
  let selection = ''
  let key = $derived(row?.key ?? '')
  let ref = $derived(roomId || row?.roomId || '')
  let version = $derived(row?.detailVersion ?? '')
  let head = $derived(row?.head ?? '')
  let synthetic = $derived(!!row?.synthetic)

  function loadDetail(row: Pick<Row, 'key' | 'synthetic'> | null | undefined, ref: string) {
    const next = row?.key && !row.synthetic && ref ? ref + '\0' + row.key : ''
    if (selection !== next) detail = null
    selection = next
    error = ''
    loading = false
    if (!row?.key || row.synthetic || !ref) return
    let cancelled = false
    loading = true
    api.changeDetail(ref, row.key).then((value: Detail) => {
      if (!cancelled) detail = value
    }).catch((err: unknown) => {
      if (!cancelled) error = errorText(err)
    }).finally(() => {
      if (!cancelled) loading = false
    })
    return () => { cancelled = true }
  }

  $effect(() => {
    revision
    version
    head
    return loadDetail({ key, synthetic }, ref)
  })
</script>

{@render children(detail, error, loading, () => revision++)}
