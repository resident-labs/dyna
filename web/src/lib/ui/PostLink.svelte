<script>
  import { postHref } from './posts.js'
  // children replaces the "Open post" label, such as a message's time.
  let { roomId, postId, onopen = null, children = null } = $props()
  let href = $derived(postHref(roomId, postId))

  function open(event) {
    if (!onopen || event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    onopen(postId)
  }
</script>

{#if href}<a {href} onclick={open} title="Open original room post" class:label={!!children}>{#if children}{@render children()}{:else}Open post <span aria-hidden="true">↗</span>{/if}</a>{/if}

<style>
  a { display: inline-flex; align-items: center; gap: var(--s1); color: var(--iris); font-size: var(--t-cap); padding: 3px 0; }
  a:hover { text-decoration: underline; }
  a.label { color: inherit; font-size: inherit; padding: 0; text-decoration: none; }
  a.label:hover { text-decoration: underline; }
</style>
