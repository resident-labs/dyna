// Browser fixture only. tests/feed.html?highlight=root&finished=line shows a
// channel feed with sample posts under those feed choices.
import '@fontsource/ibm-plex-sans/400.css'
import '@fontsource/ibm-plex-sans/500.css'
import '@fontsource/ibm-plex-sans/600.css'
import '@fontsource/jetbrains-mono/400.css'
import '../src/lib/ui/tokens.css'
import '../src/app.css'
import { mount } from 'svelte'
import Feed from '../src/lib/rooms/Feed.svelte'
import { choose } from '../src/lib/rooms/look.svelte.ts'
import { sampleRoom as room, samplePosts as posts } from '../src/lib/rooms/feedSample.ts'
import { need } from '../test/need.ts'

const q = new URLSearchParams(location.search)
for (const id of ['highlight', 'finished']) {
  const value = q.get(id)
  if (value) choose(id, value)
}
mount(Feed, { target: need(document.getElementById('app'), 'app'), props: { room, posts, view: 'general', label: 'general', onearlier: undefined, onnewer: undefined, onpresent: undefined, onaround: undefined, onacceptcall: undefined, ondeclinecall: undefined, onaddress: () => {}, onreveal: () => {}, onreply: () => {}, onforward: () => {}, onpin: () => {}, onopenfile: () => {}, ondetail: () => {}, onquestion: () => {}, ongoal: () => {} } })
