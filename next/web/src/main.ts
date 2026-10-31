// v1's faces, Latin only: other scripts fall back to the next face in the stack.
import "@fontsource/ibm-plex-sans/latin-400.css";
import "@fontsource/ibm-plex-sans/latin-500.css";
import "@fontsource/ibm-plex-sans/latin-600.css";
import "@fontsource/jetbrains-mono/latin-400.css";
import "@fontsource/jetbrains-mono/latin-500.css";
import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";
// As v1's viewport.ts: a phone's keyboard and browser bars shrink the visible area, and 100dvh follows neither on
// every browser. --app-height carries the visible height, so the composer stays above the keyboard.
const viewport = window.visualViewport;
const fit = () => document.documentElement.style.setProperty("--app-height", `${Math.round(viewport?.height ?? window.innerHeight)}px`);
fit();
viewport?.addEventListener("resize", fit);
window.addEventListener("resize", fit);
mount(App, { target: document.getElementById("app")! });
