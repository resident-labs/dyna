<script lang="ts">
  // The Settings Rules pane: the rules for every room. Room rules live in
  // each room's Rules view. roomId is empty when no room is open; the API
  // then posts no room notice for a change.
  import { onMount, untrack } from 'svelte'
  import { api } from '../../api.ts'
  import { errorText } from '../errorText.ts'
  import { nextRules } from '../rooms/rules.ts'
  import Rules from '../rooms/Rules.svelte'
  import type { RulesView } from '../api/goal.ts'

  interface Props {
    roomId?: string
    // version changes on each goals event; the pane reloads and holds a
    // new list back while an edit is open.
    version?: number
    // onmodal is true while an inline edit is open, so Esc cancels the edit
    // instead of closing Settings.
    onmodal?: (open: boolean) => void
  }
  let { roomId = '', version = 0, onmodal = () => {} }: Props = $props()
  let view = $state<RulesView | null>(null)
  let pending: RulesView | null = null
  let editing = $state(false)
  let error = $state('')

  async function load() {
    try {
      const incoming = await api.rules(roomId, true)
      const next = nextRules(view, incoming, editing)
      view = next.shown
      pending = next.pending
      error = ''
    } catch (e) {
      error = errorText(e)
    }
  }
  onMount(() => () => onmodal(false))
  $effect(() => { version; untrack(load) })
  $effect(() => {
    onmodal(editing)
    if (!editing && pending) { view = pending; pending = null }
  })
</script>

{#if error}<p class="error" role="alert">{error}</p>{/if}
<Rules {roomId} scope="global" {view} bind:editing onchanged={load} />

<style>
  .error { color: var(--coral); font-size: var(--t-ui); margin: 0 0 var(--s3); }
</style>
