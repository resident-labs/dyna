<script>
  import { markdown, turnText } from './format.js'
  import { detailHref, detailLabels } from './details.js'

  let { post, trimLead = false, onopen = null } = $props()
  let href = $derived(detailHref(post.detail, post.roomId))

  function open(e) {
    if (!onopen || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    e.preventDefault()
    e.stopPropagation()
    onopen?.(post.detail)
  }
</script>

{#if post.text}<div>{@html markdown(trimLead ? turnText(post.text) : post.text)}</div>{/if}
{#if href}
  <a class="detail" {href} onclick={open}>{detailLabels[post.detail.kind]}{post.detail.turn ? ` · turn ${post.detail.turn}` : ''}<span aria-hidden="true"> →</span></a>
{/if}

<style>
  .detail { display: inline-flex; align-items: center; min-height: 32px; gap: 3px; margin-top: 3px; font-size: var(--t-ui); font-weight: 600; }
  @media (max-width: 760px) { .detail { min-height: 44px; } }
</style>
