# Voice (experimental)

Voice lets the owner talk to a room's root by voice.
The engine runs the call itself, in Go, against a realtime voice provider. By default, it uses OpenAI Realtime through the ChatGPT sign-in of a Pi account.

## Set up

1. Sign in to ChatGPT in Settings > Accounts. Voice uses the active ChatGPT account.
2. In the app, open the room and click **Call Voice** in the room header. The button shows once a ChatGPT account is active.

## The call widget

The call is a floating widget above every view, so it stays while you move between rooms and views. Drag it by its top line, or focus that line and use the arrow keys.

- **Collapsed:** Voice, the call's length and one status line (Listening, Speaking, Thinking or Muted), with the mic button, deafen, the ⋯ microphone mode menu and hang up. A gold ring pulses while Voice speaks.
- **Expanded:** this call's conversation, read only. It shows the room's voice-call posts since the call started: your spoken lines, Voice's spoken replies, Voice's requests to root and root's posts to `@voice-call`. Each call starts a fresh conversation. After you hang up, the widget keeps the last call until you close it or call again.
- **The mic button** is the one talk control. In open mic mode, it mutes and unmutes. In push to talk mode (choose it in the ⋯ menu), hold it, or hold Space outside a text field, and Voice hears you only while you hold. Releasing it, or muting, ends your turn.
- **Deafen** silences Voice and mutes your microphone. Server speech detection stops Voice when you speak. Microphone energy does not lower playback by default.
- **Lower Voice while I talk** is an optional setting under Settings → Voice. It is off by default and applies to the next call. When enabled, Voice gets 10 dB quieter after about 150 milliseconds of sustained speech above the recent microphone noise floor. After quiet, playback returns over one second. Server interruption remains active in either mode.
- **Greeting.** Voice queues one brief greeting when the first page attaches. It uses the normal response gate and does not repeat on page reconnect.
- **Talk right away.** You may talk as soon as you press Call. What you say while Voice connects waits in the browser, up to 30 seconds, and goes to Voice once it is ready. Hanging up, a failed start or mute drops it.
- **After a reload**, a call that still runs shows at once as "Call in progress", with Rejoin and End. A page load never opens the microphone: only Rejoin does. With no page, the call ends after 60 seconds.
- One call runs at a time. If another room has the call, the widget offers to end it there and call from here.
- **On a phone** (the app's narrow layout, up to 760 px wide), the call is a full screen: a large orb shows the state by colour, shape and label, and the mic, deafen and hang up controls sit in the lower third, at least 44 px. In push to talk mode, hold the mic button. The conversation is a bottom sheet: tap its handle to open it. The chevron, or Escape, minimizes the call to a pill above the composer, which keeps the call live while you browse. Tap the pill to come back. Root's questions show on the call screen, and "Voice is calling" drops in from the top. Animations stop when the system asks for reduced motion.
- **The screen stays on** while a call runs, where the browser has the Screen Wake Lock API: a phone would otherwise lock its screen during a call. The lock returns when the owner comes back to the tab. A browser without the API, or one that refuses the lock, leaves the call as it is.
- **Root's questions:** while the widget shows, root's oldest open question to you in the call's room is a card under it. Answer with its options (or Yes and No), or type an answer. During a call, Voice also reads the question aloud, so you can answer by voice.
- **Voice calling you:** root can ask you to talk now by posting to `@voice-call`. Only root may: the internal root, or an external root holding the seat. Other agents get "only root can call the owner". The post wakes nobody. When voice is set up and no call runs, the app shows "Voice is calling" with the post as the reason. Accept starts the call in that room. During a call in that room, Voice uses the post for a short update, and it shows in the call's conversation.

## How it works

- **Speech.** The engine opens one OpenAI Realtime connection per call, to `gpt-realtime-2.1` with the voice `marin` (the provider's default). It sends the ChatGPT account's access token, with `ChatGPT-Account-ID` and `originator: codex_cli_rs` headers. A token that expires within five minutes is renewed first through the account refresh, which holds the account gate and lock and is the only writer of the saved login. The token is never logged or written by voice.
  The session is built in Go (`internal/voice/session.go` and `prompt.md`): English input transcription with a prompt that lists Dyna's words, near-field noise reduction, and semantic turn detection that neither creates nor cancels responses. The engine does both.
- **Providers.** `internal/voice/realtime` describes each provider: where to dial, its session, its sample rates, its capabilities, and how its events map to Dyna's. It is ported from qwen-audio-agent's provider architecture (Apache-2.0; see `NOTICE`). OpenAI is the one in use.
- **The call.** The owner talks to Voice, at the reserved address `<room>:voice-call`. Voice is not a session and never takes a turn. Each finished line, the owner's and Voice's, becomes a post between the owner and Voice, with `source=voice` and `spoken`. A line the transcription model writes for noise is dropped. These posts never wake root, and they stay out of the room feed. Root reads them in its `[OWNER DMS]` block at its next turn.
- **Tools.** Voice follows the provider's conversational tool model with exactly two tools, each bounded to 10 seconds:
  - `dispatch({prompt})` sends the owner's exact words to `@root`. It starts root when idle and steers its running turn otherwise. Work, status checks, later work, and answers to open questions all use it. Repeated words are valid steering. Acceptance adds no spoken acknowledgement.
  - `remain_silent({})` returns an empty tool result and requests no reply. Voice uses it when speaking would add nothing useful. A deliberate silent result also completes its pending speech batch without retries. Google Live declares quiet tools as non-blocking and completes them with [silent scheduling](https://ai.google.dev/gemini-api/docs/live-api/tools#asynchronous-function-calling), so the server does not resume generation.
- **Questions.** Voice reads root's open questions and their options. The owner's answer goes through `dispatch`. Root matches the question and records the owner's words with `dyna ask answer <id> "<owner words>"`, or uses `--choice` for an option the owner selected. Root asks for clarification when the answer is ambiguous.
- **Execution context.** While a dispatched request is open, root's own posts and posts addressed to root enter the call as `[BACKEND]` conversation text. Voice transcripts, posts addressed to `@voice-call`, and traffic between other agents stay out. They never request speech or start progress timers. Each open request shares a 4000-byte budget, including prefixes and a truncation marker. Voice receives the head as it arrives; completion, failure or stop flushes the retained latest tail and ends this window. Steering shares the same budget. Providers that cannot add text without speech, such as Doubao and MiniCPM-o, skip silent context.
- **Speaking for Dyna.** Root's explicit `@voice-call` posts and open owner questions request a response. Root's turn result stays in the room. Posts provide conversation context rather than instructions to read a full report. Voice gives only the key new point, follows the owner's update preferences, combines pending facts, and corrects only the changed point. Multiple posts per request are normal; distinct results and questions remain available.
  One response can run or be on its way. Posts arriving during speech wait together for one later reply after playback ends. They do not interrupt Voice. The existing speak queue keeps playback receipts, question replacement, and connection retries. A batch counts as handled when its audio starts or the model chooses `remain_silent`. A batch with no response is retried after 15 seconds of silence, up to 3 times; then the page marks it "not spoken". An unsupported provider marks requested speech as unspoken.
  Dyna uses explicit root posts as speech triggers instead of inferring whether arbitrary room posts are progress or final results. This adapts the provider's completion signal to the room interface without adding request tracking.
- **Barge-in.** When the owner starts to speak, the engine cancels Voice's response, tells the page to stop playback, and cuts Voice's words in the conversation to what the page played, so Voice never remembers words the owner did not hear.
- **From the app.** `VoiceStart(room)` connects the call; `VoiceStatus()` and `VoiceStop()` read and end it. The engine runs one call at a time. A start for another room fails and names the room whose call is live.
  The page opens `/api/voice/realtime?v=2` on the app origin. The engine checks the device or Connect session and the Origin, then attaches the page to the call. Version 2 sends microphone audio as binary PCM16 mono at 24 kHz; the engine converts it to provider base64. Legacy JSON microphone audio remains supported. An older engine rejects version 2 instead of dropping its audio. Controls and output keep the JSON protocol (`internal/voice/realtime/testdata/page-protocol.json`), including base64 output audio, response IDs and playback receipts. A newer page takes over the call and the older one closes with code 4001.
  A call that no page has used for 60 seconds ends, as Hang up ends it, so a closed tab leaves nothing running. A page that reloads and reconnects in time resumes the same call.
  The desktop app allows microphone requests only from the app origin, and only for audio. WebKitGTK denies a request that the host does not answer.
- **Each new call is fresh.** A call resumed after an engine restart restores saved conversation context and pending root updates.

## Speech for agents

Any session can turn text into a speech file with `dyna voice say`. It is for narration, demo voice-overs, accessibility audio and game or app characters.

```
dyna voice say "Welcome to the server!" --voice marin --style "a cheerful village helper" --out hello.wav
```

- It uses the provider in Settings > Voice, or the one you name with `--provider`. A provider that cannot speak text on request (MiniCPM-o) fails with a clear error.
- `--voice` takes the provider's preset voices only. There is no voice cloning. `--style` tells the model how to speak.
- The file is WAV (16-bit mono PCM) by default. `--format ogg` or `--format mp3` needs `ffmpeg` on the PATH, and fails with a message when it is missing. The format also follows the `--out` extension.
- The text may have up to 4000 characters, and a clip may take up to 60 seconds. The engine runs at most 2 clips at once; a third fails as busy.
- Say uses its own provider connection. It never posts to a room, never joins or changes the owner's call and keeps no transcript. Each clip uses the voice provider's account quota.
- A realtime model can change a word. The instruction to speak the text exactly usually holds, but is not guaranteed.
- The owner can turn off agent voice for `say` and `session` with **Let agents use voice** in Settings > Voice (`voice.agent`, on by default). While it is off, both fail with "the owner turned off agent voice in Settings > Voice". Owner calls are not affected.

## Live sessions for agents

`dyna voice session` is a live two-way voice conversation that a program drives. Use it for a voice character in an app or a game, a voice front end for a tool, a kiosk or phone prototype, or to test a voice app. The program writes microphone audio to the command's stdin and reads reply audio, transcripts and tool calls from its stdout. It plays the audio wherever it wants.

```
dyna voice session --prompt-file persona.md --voice marin --tools tools.json --rate 48000
```

Flags:

- `--prompt-file` sets the instructions of the voice. Without it, the voice is a short friendly assistant.
- `--voice` and `--provider` work as in `say`.
- `--tools` is a JSON file with a list of `{"name", "description", "parameters"}` tools (`parameters` is a JSON Schema object). A provider without function calling rejects it.
- `--rate` is the sample rate in hertz of the audio you send and read (default 24000, from 8000 to 96000). Dyna converts it to the provider's rate. At 48 kHz this is about 128 KB/s of base64 text each way.
- `--max-minutes` is the longest session (default 30, at most 120).

The protocol is JSON lines. Audio is base64 PCM16 mono, little endian, at `--rate`.

| In (stdin) | Meaning |
| --- | --- |
| `{"type":"audio","pcm":"…"}` | Microphone audio. |
| `{"type":"text","text":"…"}` | Add context to the conversation. The voice does not reply. |
| `{"type":"say","text":"…"}` | Make the voice speak this text. |
| `{"type":"tool_result","call_id":"…","output":"…"}` | The result of a tool call. |
| `{"type":"interrupt"}` | Stop the reply that plays. |
| `{"type":"image","path":"/abs/file.png"}` | Add a PNG or JPEG image (up to 5 MB) to the conversation, with no reply. Only providers that take images accept it (OpenAI Realtime); others return an `error` line. |

| Out (stdout) | Meaning |
| --- | --- |
| `{"type":"ready","rate":48000}` | The session is live. |
| `{"type":"audio","response_id":"…","pcm":"…"}` | Reply audio. |
| `{"type":"speech_started"}` | Someone spoke, so stop playback now. The engine has already cancelled the reply. |
| `{"type":"transcript","role":"user"\|"assistant","text":"…"}` | What was said. |
| `{"type":"tool_call","call_id":"…","name":"…","args":{…}}` | The model calls one of your tools. |
| `{"type":"response_done","response_id":"…"}` | A reply ended. |
| `{"type":"error","message":"…"}` | A problem. The session goes on, except before `ready`, where the command fails with this message. |
| `{"type":"closed","reason":"…"}` | The session ended: `input-closed`, `time-limit`, `idle`, `provider-closed`, `error` or `cancelled`. |

The provider's turn detection decides when you spoke, and the engine asks for each reply. A tool result must come within 10 seconds, or the model gets an error result and you get an `error` line. A provider that cannot take client text (MiniCPM-o) rejects `text`, `say` and `image`.

A short Python example:

```python
import base64, json, subprocess

p = subprocess.Popen(["dyna", "voice", "session", "--prompt-file", "persona.md", "--rate", "16000"],
                     stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)

def send(line):
    p.stdin.write(json.dumps(line) + "\n")
    p.stdin.flush()

send({"type": "say", "text": "Hello! How can I help?"})
for raw in p.stdout:                       # run a thread to feed microphone chunks:
    line = json.loads(raw)                 #   send({"type": "audio", "pcm": base64.b64encode(chunk).decode()})
    if line["type"] == "audio":
        play(base64.b64decode(line["pcm"]))    # your own sink
    elif line["type"] == "speech_started":
        stop_playback()
    elif line["type"] == "closed":
        break
```

Limits and behavior:

- One agent session runs at a time in the engine, separate from the 2 `say` slots. A second one fails as busy.
- A session ends at `--max-minutes`, after 60 seconds with no input audio, or when stdin ends. Send audio at least every minute (silence counts), or the session ends as idle.
- The engine holds the provider connection, so the program never sees the account token. A session never posts, wakes nobody and keeps no transcript: transcript lines go to stdout only.
- When a session starts and when it ends, its room gets a quiet notice that names the session, the provider, and at the end the time used and why it ended. The notices wake nobody. They let the owner see how much voice quota agents use. A session from the owner's own shell, outside any room, posts none.
- Barge-in truncation is approximate: the engine cancels the reply when it hears speech, and does not know how much audio your program already played.

Risks to tell your users:

- **Privacy.** Every voice you send goes to the voice provider under the owner's account. A project that lets other people talk to a session must tell them.
- **Quota.** A live session uses much more of the owner's quota than `say`. The ChatGPT sign-in is not an official Realtime credential and could stop working.

## Limits

- If the provider connection drops, the call ends; the page can start a new one.
- The ChatGPT sign-in is not an official OpenAI Realtime credential and could stop working.
