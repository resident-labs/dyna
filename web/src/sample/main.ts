// The shared UI parts in every tone, for design review and screenshots. The
// dev server serves it at /ui-sample.html; the product build leaves it out.
import '@fontsource/ibm-plex-sans/400.css'
import '@fontsource/ibm-plex-sans/500.css'
import '@fontsource/ibm-plex-sans/600.css'
import '@fontsource/jetbrains-mono/400.css'
import '../lib/ui/tokens.css'
import '../app.css'
import { mount } from 'svelte'
import UiSample from './UiSample.svelte'

const target = document.getElementById('app')
if (target === null) throw new Error('The sample root #app is missing')

export default mount(UiSample, { target })
