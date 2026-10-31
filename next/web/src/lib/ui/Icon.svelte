<script lang="ts" module>
  // Hand-drawn 16 px line icons on a 24-unit grid.
  const paths = {
    send: "M5 12h13M12 5l7 7-7 7",
    room: "M9 4 7 20M17 4l-2 16M4 9h16M3 15h16",
    person: "M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8ZM4.5 20a7.5 7.5 0 0 1 15 0",
    root: "M12 3l7.8 4.5v9L12 21l-7.8-4.5v-9ZM12 12l7.8-4.5M12 12v9M12 12 4.2 7.5",
  };
  export type IconName = keyof typeof paths;
</script>

<script lang="ts">
  let { name, size = 16 }: { name: IconName; size?: number } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
  stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={paths[name]} /></svg>

<style>
  svg { flex: none; }
</style>
