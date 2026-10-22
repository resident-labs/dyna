// Browser fixture only. tests/sidebar.html?list=folded or ?list=messages
// shows the room sidebar with that channel list.
import '@fontsource/ibm-plex-sans/400.css'
import '@fontsource/ibm-plex-sans/500.css'
import '@fontsource/ibm-plex-sans/600.css'
import '../src/lib/ui/tokens.css'
import '../src/app.css'
import { mount } from 'svelte'
import Sidebar from '../src/lib/rooms/Sidebar.svelte'
import { channelList } from '../src/lib/ui/prefs.ts'
import { need } from '../test/need.ts'

channelList.set(new URLSearchParams(location.search).get('list') ?? 'all')
const leads = ['ui-guy-right', 'engine-lead', 'settings-polish'].map((name, i) => ({ id: name, name, children: [33, 36, 2][i] ?? 0 }))
const channels = { leads, trail: () => [1] }
mount(Sidebar, { target: need(document.getElementById('app'), 'app'), props: { room: { id: 'room-a', name: 'dyna-agent', repo: '/home/user/dyna-agent' }, channels, channelUnread: { general: 4, 'channel:ui-guy-right': 7, 'channel:engine-lead': 12 }, onview: () => {}, onmember: undefined } })
