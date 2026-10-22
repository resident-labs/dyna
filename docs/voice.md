# Voice (experimental)

Voice lets the owner talk to a room's root by voice.
The engine runs the call itself, in Go, against a realtime voice provider. Today that is OpenAI Realtime, through the Codex sign-in.

## Set up

1. Sign in to Codex in Settings > Harnesses. Voice uses the active Codex account.
2. In the app, open the room and click **Call Voice** in the room header. The button shows once a Codex account is active.

## The call widget

The call is a floating widget above every view, so it stays while you move between rooms and views. Drag it by its top line, or focus that line and use the arrow keys.

- **Collapsed:** Voice, the call's length and one status line (Listening, Speaking, Thinking or Muted), with the mic button, deafen, the ⋯ microphone mode menu and hang up. A gold ring pulses while Voice speaks.
- **Expanded:** this call's conversation, read only. It shows the room's voice-call posts since the call started: your spoken lines, Voice's spoken replies, Voice's requests to root and root's posts to `@voice-call`. Each call starts a fresh conversation. After you hang up, the widget keeps the last call until you close it or call again.
- **The mic button** is the one talk control. In open mic mode, it mutes and unmutes. In push to talk mode (choose it in the ⋯ menu), hold it, or hold Space outside a text field, and Voice hears you only while you hold. Releasing it, or muting, ends your turn.
- **Deafen** silences Voice and mutes your microphone. When you start talking while Voice speaks, Voice gets quieter at once (10 dB), and stops when Voice hears your turn. If you stop, it comes back over a second.
- **Talk right away.** You may talk as soon as you press Call. What you say while Voice connects waits in the browser, up to 30 seconds, and goes to Voice once it is ready. Hanging up, a failed start or mute drops it.
- **After a reload**, a call that still runs shows at once as "Call in progress", with Rejoin and End. A page load never opens the microphone: only Rejoin does. With no page, the call ends after 60 seconds.
- One call runs at a time. If another room has the call, the widget offers to end it there and call from here.
- **On a phone** (the app's narrow layout, up to 760 px wide), the call is a full screen: a large orb shows the state by colour, shape and label, and the mic, deafen and hang up controls sit in the lower third, at least 44 px. In push to talk mode, hold the mic button. The conversation is a bottom sheet: tap its handle to open it. The chevron, or Escape, minimizes the call to a pill above the composer, which keeps the call live while you browse. Tap the pill to come back. Root's questions show on the call screen, and "Voice is calling" drops in from the top. Animations stop when the system asks for reduced motion.
- **Root's questions:** while the widget shows, root's oldest open question to you in the call's room is a card under it. Answer with its options (or Yes and No), or type an answer. During a call, Voice also reads the question aloud, so you can answer by voice.
- **Voice calling you:** root can ask you to talk now by posting to `@voice-call`. Only root may: the internal root, or an external root holding the seat. Other agents get "only root can call the owner". The post wakes nobody. When voice is set up and no call runs, the app shows "Voice is calling" with the post as the reason. Accept starts the call in that room. During a call in that room, Voice uses the post for a short update, and it shows in the call's conversation.

## How it works

- **Speech.** The engine opens one OpenAI Realtime connection per call, to `gpt-realtime-2.1` with the voice `marin` (Codex's default). It sends the Codex account's access token, with the `ChatGPT-Account-ID` and `originator` headers. A token that expires within five minutes is refreshed first. The token is never logged or written by voice.
  The session is built in Go (`internal/voice/session.go` and `prompt.md`): English input transcription with a prompt that lists Dyna's words, near-field noise reduction, and semantic turn detection that neither creates nor cancels responses. The engine does both.
- **Providers.** `internal/voice/realtime` describes each provider: where to dial, its session, its sample rates, its capabilities, and how its events map to Dyna's. It is ported from qwen-audio-agent's provider architecture (Apache-2.0; see `NOTICE`). OpenAI is the one in use.
- **The call.** The owner talks to Voice, at the reserved address `<room>:voice-call`. Voice is not a session and never takes a turn. Each finished line, the owner's and Voice's, becomes a post between the owner and Voice, with `source=voice` and `spoken`. A line the transcription model writes for noise is dropped. These posts never wake root, and they stay out of the room feed. Root reads them in its `[OWNER DMS]` block at its next turn.
- **Tools.** Voice follows Codex's conversational tool model with exactly two tools, each bounded to 10 seconds:
  - `dispatch({prompt})` sends the owner's exact words to `@root`. It starts root when idle and steers its running turn otherwise. Work, status checks, later work, and answers to open questions all use it. Repeated words are valid steering. Acceptance adds no spoken acknowledgement.
  - `remain_silent({})` returns an empty tool result and requests no reply. Voice uses it when speaking would add nothing useful. A deliberate silent result also completes its pending speech batch without retries. Google Live declares quiet tools as non-blocking and completes them with [silent scheduling](https://ai.google.dev/gemini-api/docs/live-api/tools#asynchronous-function-calling), so the server does not resume generation.
- **Questions.** Voice reads root's open questions and their options. The owner's answer goes through `dispatch`. Root matches the question and records the owner's words with `dyna ask answer <id> "<owner words>"`, or uses `--choice` for an option the owner selected. Root asks for clarification when the answer is ambiguous.
- **Execution context.** While a dispatched request is open, messages between root and its agents enter the call as `[BACKEND]` conversation text. They never request speech or start progress timers. A root turn's completion ends this context window. Providers that cannot add text without speech, such as Doubao and MiniCPM-o, skip silent context.
- **Speaking for Dyna.** Root's explicit `@voice-call` posts and open owner questions request a response. Root's turn result stays in the room. Posts provide conversation context rather than instructions to read a full report. Voice gives only the key new point, follows the owner's update preferences, combines pending facts, and corrects only the changed point. Multiple posts per request are normal; distinct results and questions remain available.
  One response can run or be on its way. Posts arriving during speech wait together for one later reply after playback ends. They do not interrupt Voice. The existing speak queue keeps playback receipts, question replacement, and connection retries. A batch counts as handled when its audio starts or the model chooses `remain_silent`. A batch with no response is retried after 15 seconds of silence, up to 3 times; then the page marks it "not spoken". An unsupported provider marks requested speech as unspoken.
  Dyna uses explicit root posts as speech triggers instead of inferring whether arbitrary room posts are progress or final results. This adapts Codex's completion signal to the room interface without adding request tracking.
- **Barge-in.** When the owner starts to speak, the engine cancels Voice's response, tells the page to stop playback, and cuts Voice's words in the conversation to what the page played, so Voice never remembers words the owner did not hear.
- **From the app.** `VoiceStart(room)` connects the call; `VoiceStatus()` and `VoiceStop()` read and end it. The engine runs one call at a time. A start for another room fails and names the room whose call is live.
  The page opens a WebSocket at `/api/voice/realtime` on the app origin. The engine checks the device or Connect session and the Origin, as for every app stream, and attaches the page to the call. The page and the engine speak a small protocol of JSON messages (`internal/voice/realtime/testdata/page-protocol.json`): base64 PCM16 audio at 24 kHz both ways, mute, cancel, playback receipts and hangup from the page; state, audio, audio done, clear, the owner's partial words, not-spoken items and errors from the engine. A newer page takes over the call and the older one closes with code 4001.
  A call that no page has used for 60 seconds ends, as Hang up ends it, so a closed tab leaves nothing running. A page that reloads and reconnects in time resumes the same call.
  The desktop app allows microphone requests only from the app origin, and only for audio. WebKitGTK denies a request that the host does not answer.
- **Each call is fresh.** Every call is a new conversation with the provider.

## Limits

- If the provider connection drops, the call ends; the page can start a new one.
- The Codex sign-in is not an official OpenAI Realtime credential and could stop working.
