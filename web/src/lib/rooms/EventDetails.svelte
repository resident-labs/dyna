<script lang="ts">
  // The agent text of a system event, behind a toggle. A card shows the facts;
  // a session DM keeps the input the agent received one click away, collapsed,
  // so an owner who needs to see exactly what the agent read still can.
  let { text }: { text?: string | null } = $props()
  let open = $state(false)
</script>

{#if text?.trim()}
  <div class="event-details">
    <button class="toggle" type="button" aria-expanded={open} onclick={() => (open = !open)}>{open ? 'Hide details' : 'Details'}</button>
    {#if open}<pre class="text">{text}</pre>{/if}
  </div>
{/if}

<style>
  .event-details { margin-top: var(--s2); }
  .toggle {
    padding: 2px 0;
    border: 0;
    background: none;
    color: var(--muted);
    font: inherit;
    font-size: var(--t-sm);
    cursor: pointer;
    transition: color var(--motion-fast) var(--ease);
  }
  .toggle:hover { color: var(--ink-2); text-decoration: underline; }
  .text {
    margin: var(--s1) 0 0;
    padding: var(--s2);
    max-height: 240px;
    overflow: auto;
    background: var(--side);
    border-radius: var(--r-sm);
    color: var(--muted);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font: var(--t-sm)/1.5 var(--mono);
  }
</style>
