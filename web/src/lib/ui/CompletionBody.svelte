<script lang="ts">
  import { markdown, turnText } from './format.ts'
  import { detailHref, detailLabels } from './details.ts'
  import type { Post, PostDetail } from '../api/room.ts'

  interface Props {
    post: Post
    trimLead?: boolean
    onopen?: ((detail: PostDetail) => void) | null
  }
  let { post, trimLead = false, onopen = null }: Props = $props()
  let href = $derived(detailHref(post.detail, post.roomId))

  // detailLabels keys a detail by its kind, and a stored kind is a plain string,
  // so each known kind names its own label.
  function detailLabel(detail: PostDetail): string {
    if (detail.kind === 'shell') return detailLabels.shell
    if (detail.kind === 'run') return detailLabels.run
    if (detail.kind === 'session') return detailLabels.session
    return ''
  }

  function open(e: MouseEvent) {
    if (!onopen || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    e.preventDefault()
    e.stopPropagation()
    if (post.detail) onopen(post.detail)
  }
</script>

{#if post.text}<div>{@html markdown(trimLead ? turnText(post.text) : post.text)}</div>{/if}
{#if href && post.detail}
  <a class="detail" {href} onclick={open}>{detailLabel(post.detail)}{post.detail.turn ? ` · turn ${post.detail.turn}` : ''}<span aria-hidden="true"> →</span></a>
{/if}

<style>
  .detail { display: inline-flex; align-items: center; min-height: 32px; gap: 3px; margin-top: 3px; font-size: var(--t-ui); font-weight: 600; }
  @media (max-width: 760px) { .detail { min-height: 44px; } }
</style>
