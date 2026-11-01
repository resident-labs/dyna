<script lang="ts" module>
  // Hand-drawn 16 px line icons on a 24-unit grid.
  const paths = {
    send: "M5 12h13M12 5l7 7-7 7",
    stop: "M7 7h10v10H7z",
    warning: "M12 3 2 20h20zM12 9v5M12 17h.01",
    pause: "M9 5v14M15 5v14",
    monitor: "M4 4h16a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1ZM8 20h8M12 16v4",
    home: "M4 10.5 12 4l8 6.5V20h-5v-6H9v6H4z",
    room: "M9 4 7 20M17 4l-2 16M4 9h16M3 15h16",
    person: "M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8ZM4.5 20a7.5 7.5 0 0 1 15 0",
    people: "M9 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7ZM2.5 20a6.5 6.5 0 0 1 13 0M16 4.3a3.5 3.5 0 0 1 0 6.4M18.5 14a6.5 6.5 0 0 1 3 6",
    terminal: "M4 4h16a1 1 0 0 1 1 1v14a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1ZM7 9l3 3-3 3M13 15h4",
    prompt: "M5 6l6 6-6 6M13 18h6",
    chevron: "M6 9l6 6 6-6",
    menu: "M4 7h16M4 12h16M4 17h16",
    chat: "M5 5h14a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1h-8l-4 3.5V16H5a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1Z",
    back: "M19 12H5M11 18l-6-6 6-6",
    more: "M5 11a1 1 0 1 0 0 2 1 1 0 0 0 0-2ZM12 11a1 1 0 1 0 0 2 1 1 0 0 0 0-2ZM19 11a1 1 0 1 0 0 2 1 1 0 0 0 0-2Z",
    close: "M6 6l12 12M18 6 6 18",
    dock: "M4 5h16v14H4ZM15 5v14",
    dockBottom: "M4 5h16v14H4ZM4 14h16",
    forward: "M15 6l6 6-6 6M20 12h-9a6 6 0 0 0-6 6v1",
    undo: "M9 7 4 12l5 5M4 12h9a6 6 0 0 1 6 6",
    copy: "M10 9h9a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1h-9a1 1 0 0 1-1-1v-9a1 1 0 0 1 1-1ZM5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1",
    check: "M5 12.5l4.5 4.5L19 7",
    shield: "M12 3 20 6v5c0 5-3.5 8-8 10-4.5-2-8-5-8-10V6z",
    clip: "m15.5 7.5-6.4 6.4a1.8 1.8 0 0 0 2.5 2.5l6.7-6.7a3.6 3.6 0 0 0-5.1-5.1l-6.9 6.9a5.4 5.4 0 0 0 7.6 7.6l6.1-6.1",
    plus: "M12 5v14M5 12h14",
    undock: "M4 5h16v14H4ZM11 11h6v5h-6Z",
    maximize: "M14 4h6v6M10 20H4v-6M20 4l-6 6M4 20l6-6",
    external: "M13 4h7v7M20 4l-9 9M19 14v5H5V5h5",
    restore: "M20 4l-6 6M14 5v5h5M4 20l6-6M10 19v-5H5",
    root: "M12 3l7.8 4.5v9L12 21l-7.8-4.5v-9ZM12 12l7.8-4.5M12 12v9M12 12 4.2 7.5",
    gear: "M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2ZM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6Z",
    devices: "M3 5h13v10H3ZM2 18h12M18 9h3a1 1 0 0 1 1 1v8a1 1 0 0 1-1 1h-3a1 1 0 0 1-1-1v-8a1 1 0 0 1 1-1Z",
    laptop: "M5 5h14a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1ZM2 19h20",
    phone: "M9 3h6a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H9a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2ZM11 18h2",
    forwardChevron: "M9 6l6 6-6 6",
    backChevron: "M15 6l-6 6 6 6",
    palette: "M12 3a9 9 0 0 0 0 18c1 0 1.7-.8 1.7-1.7 0-.4-.2-.8-.4-1.1-.3-.3-.4-.7-.4-1.1 0-.9.7-1.6 1.6-1.6H16a5 5 0 0 0 5-5C21 6.5 17 3 12 3ZM7.5 12h.01M9.5 7.5h.01M14.5 7.5h.01",
    plug: "M9 3v4M15 3v4M7 7h10v4a5 5 0 0 1-10 0ZM12 16v5",
    inbox: "M3 13h5l1.5 2.5h5L16 13h5M5.5 5h13L21 13v6H3v-6Z",
    file: "M6 4h8l4 4v12H6ZM14 4v4h4",
    edit: "M5 19h3.5L18.5 9 15 5.500l-10 10ZM13 7.500l3.500 3.500",
    write: "M6 4h12v16H6ZM9.500 8.500h5M9.500 12h5M9.500 15.500h2.500",
    search: "M11 16.500a5.500 5.500 0 1 0 0-11 5.500 5.500 0 0 0 0 11ZM15 15l4.500 4.500",
    list: "M9 7h11M9 12h11M9 17h11M4.500 7h.01M4.500 12h.01M4.500 17h.01",
    globe: "M12 20a8 8 0 1 0 0-16 8 8 0 0 0 0 16ZM4 12h16M12 4c2.500 2.500 2.500 13.500 0 16M12 4c-2.500 2.500-2.500 13.500 0 16",
    keyboard: "M3 7h18a1 1 0 0 1 1 1v8a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V8a1 1 0 0 1 1-1ZM6 10.500h.01M10 10.500h.01M14 10.500h.01M18 10.500h.01M7.500 14h9",
    tool:"M5 5h14v14H5ZM9.500 9.500h5M9.500 14.500h5",
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
