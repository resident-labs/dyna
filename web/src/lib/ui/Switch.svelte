<script lang="ts">
  interface Props {
    on?: boolean
    // label names the switch; a row that already names it may leave it out.
    label?: string
    disabled?: boolean
    onchange: (on: boolean) => void
  }
  let { on = false, label, disabled = false, onchange }: Props = $props()
</script>

<button class="switch" class:on role="switch" aria-checked={on} aria-label={label} {disabled} onclick={() => onchange(!on)}><span></span></button>

<style>
  .switch { flex: none; width: 42px; height: 24px; border-radius: 12px; background: #4a474c; position: relative; transition: background var(--motion-fast); }
  .switch span { position: absolute; top: 3px; left: 3px; width: 18px; height: 18px; border-radius: 50%; background: #fff; transition: left var(--motion-fast); }
  .switch.on { background: #4f9a5a; }
  .switch.on span { left: 21px; }
  .switch:disabled { opacity: 0.5; cursor: default; }
  @media (max-width: 760px) {
    /* A 44 px touch target without stretching the pill. */
    .switch::before { content: ''; position: absolute; inset: -10px -4px; }
  }
</style>
