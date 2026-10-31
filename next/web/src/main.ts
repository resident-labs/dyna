// v1's faces, Latin only: other scripts fall back to the next face in the stack.
import "@fontsource/ibm-plex-sans/latin-400.css";
import "@fontsource/ibm-plex-sans/latin-500.css";
import "@fontsource/ibm-plex-sans/latin-600.css";
import "@fontsource/ibm-plex-sans/latin-700.css";
import "@fontsource/jetbrains-mono/latin-400.css";
import "@fontsource/jetbrains-mono/latin-500.css";
import { mount } from "svelte";
import App from "./App.svelte";
import ConnectScreen from "./lib/ui/ConnectScreen.svelte";
import "./app.css";
import "./lib/theme.svelte.ts";
// As v1's viewport.ts: a phone's keyboard and browser bars shrink the visible area, and 100dvh follows neither on
// every browser. --app-height carries the visible height, so the composer stays above the keyboard.
const viewport = window.visualViewport;
// A hidden or prerendered tab may report 0, as v1 guards. --keyboard is the covered height: iOS keeps the bottom
// safe-area inset while the keyboard is open, and the shell drops that padding then. Pinch zoom also shrinks the
// visible height; the shell keeps its size while zoomed, so the reader pans over it, and fits again at scale 1.
const fit = () => {
  const height = viewport?.height ?? window.innerHeight, style = document.documentElement.style;
  if (height <= 0 || Math.abs((viewport?.scale ?? 1) - 1) > 0.01) return;
  style.setProperty("--app-height", `${Math.round(height)}px`);
  style.setProperty("--keyboard", `${Math.max(0, Math.round(window.innerHeight - height))}px`);
};
fit();
viewport?.addEventListener("resize", fit);
window.addEventListener("resize", fit);
const target = document.getElementById("app")!;
if (location.pathname === "/connect" && location.hash) mount(ConnectScreen, { target, props: { landing: true } });
else mount(App, { target });
