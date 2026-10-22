<script>
  // A thumbtack, drawn like the Reply icon: 1.8 stroke, round caps. A filled
  // one marks a pinned post.
  let { size = 18, filled = false } = $props()
</script>

<svg viewBox="0 0 20 20" width={size} height={size} aria-hidden="true">
  <path d="M7 3h6l-.8 5.2L15 11H5l2.8-2.8L7 3ZM10 11v6" fill={filled ? 'currentColor' : 'none'} stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
</svg>
