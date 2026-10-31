// v1's faces, Latin only: other scripts fall back to the next face in the stack.
import "@fontsource/ibm-plex-sans/latin-400.css";
import "@fontsource/ibm-plex-sans/latin-500.css";
import "@fontsource/ibm-plex-sans/latin-600.css";
import "@fontsource/ibm-plex-sans/latin-700.css";
import "@fontsource/jetbrains-mono/latin-400.css";
import "@fontsource/jetbrains-mono/latin-500.css";
import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";
mount(App, { target: document.getElementById("app")! });
