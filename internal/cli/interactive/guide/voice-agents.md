# Voice agents

Read this before you build speech or a live voice conversation into an app, a game or a tool.
Both commands use the owner's voice provider account and quota. Neither one posts to a room.

## Pick a command

- `dyna voice say "text" --voice marin --style "calm guide" --out hello.wav` turns text into a speech file.
  Use it for narration, voice-overs, accessibility audio and fixed character lines. WAV is the default;
  `--format ogg` or `mp3` needs `ffmpeg`. Text is at most 4000 characters. At most 2 clips run at once.
- `dyna voice session` is a live two-way conversation. Use it when a person talks and the voice answers:
  a character, a kiosk, a phone line, a voice front end for a tool. Your program writes microphone audio to
  stdin and reads reply audio, transcripts and tool calls from stdout. At most one session runs at a time;
  a second one fails as busy.

If you do not need to listen, use `say`. A session costs much more quota.

## Session protocol

```sh
dyna voice session --prompt-file persona.md --voice marin --tools tools.json --rate 16000
```

`--rate` is the sample rate of your audio (8000 to 96000, default 24000). `--max-minutes` defaults to 30 (at most 120).
`--vad-eagerness` is `low`, `medium`, `high` or `auto`. It sets how soon turn detection ends a turn. OpenAI Realtime supports it. `high` ends sooner. `low` waits longer. Omit it to keep the provider default. A provider that cannot set it rejects the flag, and the session does not start.
`--turns` is `auto` (the default) or `manual`. `auto` keeps the provider's turn detection. `manual` turns that detection off. OpenAI Realtime then gets `turn_detection` null. Only `{"type":"commit"}` ends a turn and asks for one reply. `--vad-eagerness` conflicts with `manual`, and the session does not start. A provider that cannot turn detection off rejects `--turns manual`, and the session does not start.
`--tools` is a JSON list of `{"name","description","parameters","after"}`. `parameters` is a JSON Schema.
`after` says what a result does. `reply` (the default) asks the model to speak. `later` keeps the result
as context and asks for no reply, as Dyna Voice's dispatch does. `silent` gives the model an empty result
and asks for no reply, as Dyna Voice's remain_silent does. The session passes those marks to the
provider, as Dyna Voice does. Any other value is an error, and the session does not start.
Every line is one JSON object. Audio is base64 PCM16 mono, little endian, at `--rate`.

| In (your stdin) | Meaning |
| --- | --- |
| `{"type":"audio","pcm":"<base64>"}` | Microphone audio. |
| `{"type":"text","text":"…"}` | Add context. The voice does not reply, unless the line also has `"respond":true`. That asks for one reply and waits for speech that still plays, the same way `say` mode `append` does. |
| `{"type":"say","text":"…","mode":"append"}` | Make the voice speak this text. `mode` is `append` (default: wait for the speech that plays) or `replace` (cancel it and speak now). |
| `{"type":"tool_result","call_id":"…","output":"…"}` | Answer a tool call. |
| `{"type":"interrupt"}` | Stop the reply that plays. |
| `{"type":"commit"}` | End the turn now, if new speech arrived since the last commit. Keepalive silence does not count. The engine commits that speech and asks for one reply. If the provider already ended the turn, or no new audio arrived, the line does nothing: no error and no second reply. If a reply is already in flight, the new reply waits, the same way a text line with `"respond":true` does. With `--turns manual`, this line is the only way a turn ends. OpenAI Realtime can do this. A provider that cannot sends an `error` line, and the session goes on. |
| `{"type":"image","path":"/abs/file.png"}` | Add a PNG or JPEG (at most 5 MB). No reply. OpenAI Realtime only; others send an `error` line. |

| Out (your stdout) | Meaning |
| --- | --- |
| `{"type":"ready","rate":16000}` | The session is live. |
| `{"type":"audio","response_id":"…","pcm":"<base64>"}` | Reply audio. |
| `{"type":"speech_started"}` | Someone spoke. Stop playback now. |
| `{"type":"transcript","role":"user"\|"assistant","text":"…"}` | What was said. |
| `{"type":"tool_call","call_id":"…","name":"…","args":{…}}` | The model calls a tool. |
| `{"type":"response_done","response_id":"…"}` | A reply ended. |
| `{"type":"error","message":"…"}` | A problem. The session goes on, except before `ready`. |
| `{"type":"closed","reason":"…"}` | `input-closed`, `time-limit`, `idle`, `provider-closed`, `error` or `cancelled`. |

Rules to design for:

- **Turn-taking.** With `--turns auto`, the provider detects when people speak and the engine asks for each reply. Send audio all the time,
  also silence, so it can see where speech ends. If your program already knows the person stopped talking, send `commit` instead of waiting for turn detection. A `commit` after the provider already ended that turn does nothing, even if you kept sending silence, so it will not answer twice. With `--turns manual`, the provider does not end the turn. Send `commit` when the person has finished. That is the only end, and it asks for one reply. A session with no input audio for 60 seconds ends as `idle`.
  When stdin ends, the session ends as `input-closed`: keep stdin open until you have the last `response_done`.
- **Barge-in.** On `speech_started`, drop the audio you have queued and stop playback at once. The engine has
  already cancelled the reply. Truncation is approximate, because the engine does not know how much you played.
  With `--turns manual`, audio that arrives while a reply plays does nothing automatic. The engine does not cancel that reply, and it does not send `speech_started` for that audio. Send `{"type":"interrupt"}` to stop playback. Then send `commit` when the person has finished the new turn.
- **Tool calls.** Answer each `tool_call` with a `tool_result` that has the same `call_id`, within 10 seconds.
  After that, with no result, the model gets an error and you get an `error` line. That error still asks
  for a reply, even on a `later` or `silent` tool. Do slow work in the background and answer fast, for
  example "started". A `later` tool keeps that word as context and does not speak. Tell the model the real
  result later with `{"type":"text","text":"…","respond":true}` or `say`.
- **Images.** Write the file, send `{"type":"image","path":…}`, then ask about it with `text` or let the person ask.
- **`ready` first.** Do not send audio before `ready`; a start failure prints the error and exits non-zero.

## Speaker mode: a stream of lines

An NPC, narrator or kiosk speaks many lines and must not overlap clips. Open one session with no
microphone, and send each line as `say`. Use `"mode":"replace"` for a line that makes the old one stale; the
engine cancels the reply, sends `speech_started` (drop your buffered audio), and sends no more audio of the old
reply. Use `append` for lines that must all be heard in order. One open session keeps the voice and style
steady and has low latency per line. It needs a provider that takes text (not audio-only ones).

## Example bridge: Python

It sends a 16 kHz mono 16-bit WAV, answers one tool call and writes the reply to a WAV.
Create `tools.json` with `[{"name":"get_time","description":"Current time","parameters":{"type":"object","properties":{}}}]`.
Run `python3 bridge.py in.wav out.wav`. Set `VOICE_CMD` only to test with a stand-in program.

```python
import base64, json, os, subprocess, sys, threading, time, wave

RATE = 16000
CMD = os.environ.get("VOICE_CMD", f"dyna voice session --tools tools.json --rate {RATE}").split()
src, dst = sys.argv[1:3]
p = subprocess.Popen(CMD, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
lock, done = threading.Lock(), threading.Event()

def send(msg):
    with lock:
        p.stdin.write(json.dumps(msg) + "\n")
        p.stdin.flush()

def audio(pcm):
    send({"type": "audio", "pcm": base64.b64encode(pcm).decode()})

def feed():
    with wave.open(src) as w:
        assert (w.getnchannels(), w.getsampwidth(), w.getframerate()) == (1, 2, RATE), "need 16 kHz mono 16-bit WAV"
        while chunk := w.readframes(RATE // 50):  # 20 ms
            audio(chunk)
            time.sleep(0.02)
    while not done.is_set():                      # silence keeps the session alive
        audio(bytes(RATE // 50 * 2))
        time.sleep(0.02)

feeder = threading.Thread(target=feed, daemon=True)
out, saw_tool, answered = bytearray(), False, False
for raw in p.stdout:
    msg = json.loads(raw)
    kind = msg["type"]
    if kind == "ready":
        feeder.start()
    elif kind == "audio":
        out += base64.b64decode(msg["pcm"])
    elif kind == "tool_call":
        saw_tool = True
        send({"type": "tool_result", "call_id": msg["call_id"], "output": time.strftime("%H:%M")})
        answered, out = True, bytearray()         # keep only the reply after the tool result
    elif kind == "response_done" and out and (answered or not saw_tool):
        break
    elif kind == "error":
        print("error:", msg["message"], file=sys.stderr)
    elif kind == "closed":
        break
done.set()
feeder.join()
p.stdin.close()
with wave.open(dst, "wb") as w:
    w.setnchannels(1); w.setsampwidth(2); w.setframerate(RATE)
    w.writeframes(bytes(out))
p.wait()
```

## Example bridge: Node

Same behavior, Node 18 or newer, no packages. Run `node bridge.mjs in.wav out.wav`.

```js
import { spawn } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { createInterface } from 'node:readline';
import { setTimeout as sleep } from 'node:timers/promises';

const RATE = 16000;
const [cmd, ...args] = (process.env.VOICE_CMD ?? `dyna voice session --tools tools.json --rate ${RATE}`).split(' ');
const [src, dst] = process.argv.slice(2);
const p = spawn(cmd, args, { stdio: ['pipe', 'pipe', 'inherit'] });
const send = (msg) => p.stdin.write(JSON.stringify(msg) + '\n');
const audio = (pcm) => send({ type: 'audio', pcm: pcm.toString('base64') });

function readWav(file) {                       // 16-bit mono PCM only
  const b = readFileSync(file);
  for (let i = 12; i + 8 <= b.length; i += 8 + b.readUInt32LE(i + 4)) {
    if (b.toString('ascii', i, i + 4) === 'fmt ' && (b.readUInt16LE(i + 10) !== 1 || b.readUInt16LE(i + 22) !== 16 || b.readUInt32LE(i + 12) !== RATE))
      throw new Error('need 16 kHz mono 16-bit WAV');
    if (b.toString('ascii', i, i + 4) === 'data') return b.subarray(i + 8, i + 8 + b.readUInt32LE(i + 4));
  }
  throw new Error('no data chunk');
}

let finished = false;
async function feed() {
  const pcm = readWav(src), step = (RATE / 50) * 2;      // 20 ms
  for (let o = 0; o < pcm.length && !finished; o += step) { audio(pcm.subarray(o, o + step)); await sleep(20); }
  while (!finished) { audio(Buffer.alloc(step)); await sleep(20); }  // silence keeps the session alive
}

let out = [], sawTool = false, answered = false;
for await (const line of createInterface({ input: p.stdout })) {
  const msg = JSON.parse(line);
  if (msg.type === 'ready') feed();
  else if (msg.type === 'audio') out.push(Buffer.from(msg.pcm, 'base64'));
  else if (msg.type === 'tool_call') {
    sawTool = answered = true;
    send({ type: 'tool_result', call_id: msg.call_id, output: new Date().toTimeString().slice(0, 5) });
    out = [];                                             // keep only the reply after the tool result
  } else if (msg.type === 'error') console.error('error:', msg.message);
  else if (msg.type === 'closed' || (msg.type === 'response_done' && out.length && (answered || !sawTool))) break;
}
finished = true;
p.stdin.end();
const pcm = Buffer.concat(out), h = Buffer.alloc(44);
h.write('RIFF', 0); h.writeUInt32LE(36 + pcm.length, 4); h.write('WAVEfmt ', 8); h.writeUInt32LE(16, 16);
h.writeUInt16LE(1, 20); h.writeUInt16LE(1, 22); h.writeUInt32LE(RATE, 24); h.writeUInt32LE(RATE * 2, 28);
h.writeUInt16LE(2, 32); h.writeUInt16LE(16, 34); h.write('data', 36); h.writeUInt32LE(pcm.length, 40);
writeFileSync(dst, Buffer.concat([h, pcm]));
```

## Connect an audio source

Pick `--rate` to match your source when you can, so you do not resample. Send chunks of 10 to 40 ms.
Keep a jitter buffer for output and play it at the sample rate. Clear that buffer on `speech_started`.

- **Game voice chat plugin** (a server or client mod). Voice chat mods usually give 48 kHz mono PCM16 per speaker,
  or Opus frames. Decode Opus to PCM16 first (libopus). A mod in Java, Kotlin or C# can start the command
  and use the same pipes. Send reply audio back as the mod's own voice source, such as a positional speaker
  entity. Use `--rate 48000` to skip resampling.
- **Discord.** Voice receive gives Opus frames at 48 kHz stereo, one stream per speaker. Decode and downmix to
  mono. Send reply audio as 48 kHz stereo Opus frames, so duplicate the mono channel. The bot library does the encoding.
- **Browser through WebRTC.** `getUserMedia` plus an `AudioWorklet` gives Float32 samples at the browser's
  rate (often 48 kHz). Convert to Int16 (`clamp(x) * 32767`) and send binary frames over a WebSocket to your small
  server, which wraps them in `audio` lines. Play replies through an `AudioContext` queue. Turn on echo
  cancellation (`echoCancellation: true`), or the voice hears itself and cuts itself off.
- **Phone line.** SIP or a telephony service gives 8 kHz mono G.711 µ-law or A-law. Decode to PCM16 (a lookup
  table or `audioop.ulaw2lin`), run with `--rate 8000`, and encode replies back to µ-law. Phone audio is poor, so
  keep the persona short and clear.
- **Local microphone and speaker.** Use `arecord -f S16_LE -r 16000 -c 1 -t raw` or `parec --format=s16le --rate=16000 --channels=1`
  as the input, and `aplay -f S16_LE -r 16000 -c 1 -t raw` or `pacat` as the output. Use headphones, or the voice hears itself.
- **Several speakers.** The session takes one audio stream, so mix all speakers: add the Int16 samples of the same
  time slice and clamp to the range -32768 to 32767. The voice cannot tell speakers apart, so name them in a `text`
  line ("Alex joined") or use push to talk per person. Send silence when nobody talks.
- **A tool that returns data.** For tools that need the app (a map lookup, a game command), declare them in
  `tools.json` and run them in your program when the `tool_call` arrives.

For a test of your app without a person, pipe a WAV file as above, or use `say` to make test inputs.

## Privacy, quota and the owner

- **Privacy.** All voices you send go to the voice provider under the owner's account. A project that lets other
  people talk to a session must tell them before they speak, for example with a sign or a chat message.
- **Quota.** A session uses the owner's voice quota. Keep it short: set `--max-minutes`, end the session when
  nobody speaks for a while, and do not leave one open. The ChatGPT sign-in is not an official Realtime
  credential and could stop working.
- **Owner control.** The owner can turn off agent voice for both commands (**Let agents use voice** in Settings >
  Voice). Then they fail with "the owner turned off agent voice in Settings > Voice". Do not work around it.
- **Room notices.** When a session starts and ends, its room gets a quiet notice with the provider, time used and
  reason. These notices wake nobody and show the owner how much agents use voice.
- A session never posts, never joins the owner's call and keeps no transcript. Save transcripts yourself if you need them.

## Related recipes

- To show the app or game to the owner while the voice talks, open it in a computer: read `dyna guide computers`
  (section "Desktop and GPU apps").
- To let the owner pick a voice or persona before you start, ask with a form card: read `dyna guide posting`
  (section "Interactive cards").
