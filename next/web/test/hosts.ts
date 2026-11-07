import { mount } from "svelte";
import "@fontsource/ibm-plex-sans/latin-400.css";
import "@fontsource/ibm-plex-sans/latin-500.css";
import "@fontsource/ibm-plex-sans/latin-600.css";
import "@fontsource/jetbrains-mono/latin-400.css";
import "../src/app.css";
import "../src/lib/theme.svelte.ts";
import HostsFixture from "./HostsFixture.svelte";
mount(HostsFixture, { target: document.getElementById("app")! });
