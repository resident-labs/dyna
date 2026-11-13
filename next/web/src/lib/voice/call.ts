import { openAudio, micError, type AudioHandle } from './audio.ts';
import { bytesToBase64, encodePCMFrame } from './pcm.ts';

export type CallState = { status: string; level?: number; active: boolean; muted: boolean; transcript: { role: string; text: string }[] };
// An assistant transcript with item_id precedes each item's binary audio;
// the engine sends its final text later.
export class VoiceCall {
  state: CallState = { status: '', active: false, muted: false, transcript: [] };
  private audio?: AudioHandle;
  private socket?: WebSocket;
  private working = false;
  private playing = new Set<string>();
  private item = '';
  private generation = 0;
  private receiptTimer?: ReturnType<typeof setInterval>;
  private changed: (state: CallState) => void;
  constructor(changed: (state: CallState) => void) { this.changed = changed; }
  private update() { this.changed({ ...this.state, transcript: [...this.state.transcript] }); }
  async start(room: string) {
    if (this.state.active) return;
    const generation = ++this.generation;
    this.state = { status: 'Opening microphone…', active: true, muted: false, transcript: [] }; this.update();
    try {
      const drained = (id: string) => {
        this.playing.delete(id);
        if (!this.playing.size && this.state.active) { this.state.status = this.working ? 'Working' : 'Listening'; this.update(); }
      };
      const audio = await openAudio((frame, rate) => {
        this.state.level = this.state.muted ? 0 : Math.min(1, Math.sqrt(frame.reduce((sum, v) => sum + v * v, 0) / frame.length) * 6);
        this.update();
        if (!this.state.muted && this.socket?.readyState === WebSocket.OPEN && this.state.status !== 'Connecting…') this.socket.send(encodePCMFrame(frame, rate, 24000));
      }, { started: id => { this.playing.add(id); this.state.status = 'Speaking'; this.update(); }, progress() {}, ended: drained, cancelled: drained });
      if (generation !== this.generation) { audio.close(); return; }
      this.audio = audio;
      this.state.status = 'Connecting…'; this.update();
      const url = new URL('/v1/voice', location.href); url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'; url.searchParams.set('room', room);
      const ws = this.socket = new WebSocket(url); ws.binaryType = 'arraybuffer';
      this.receiptTimer = setInterval(() => {
        if (this.socket === ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'played', item_id: '', audio_end_ms: 0 }));
      }, 15000);
      ws.onmessage = event => {
        if (this.socket !== ws || generation !== this.generation) return;
        if (event.data instanceof ArrayBuffer) {
          if (this.item) audio.play(bytesToBase64(new Uint8Array(event.data)), 24000, this.item);
          return;
        }
        const msg = JSON.parse(event.data);
        switch (msg.type) {
          case 'ready': this.state.status = 'Listening'; break;
          case 'speech_started': audio.stopPlayback('interrupted'); this.item = ''; this.working = false; this.state.status = 'Listening'; break;
          case 'transcript':
            if (msg.role === 'assistant' && msg.item_id) { this.item = msg.item_id; }
            if (msg.text) this.state.transcript.push({ role: msg.role, text: msg.text });
            break;
          case 'response_done':
            this.working = msg.working === true;
            if (this.item) audio.done(this.item);
            if (!this.playing.size) this.state.status = this.working ? 'Working' : 'Listening';
            break;
          case 'closed': this.stop(msg.reason || 'Call ended'); break;
        }
        this.update();
      };
      ws.onerror = () => { if (this.socket === ws && generation === this.generation) this.stop('The connection dropped. Try calling again.'); };
      ws.onclose = () => { if (this.socket === ws) this.stop('The connection dropped. Try calling again.'); };
    } catch (error) { if (generation === this.generation) this.stop(micError(error)); }
  }
  mute() { this.state.muted = !this.state.muted; this.update(); }
  stop(status = 'Call ended') {
    ++this.generation;
    clearInterval(this.receiptTimer); this.receiptTimer = undefined;
    this.audio?.close(); this.audio = undefined;
    const ws = this.socket; this.socket = undefined; ws?.close();
    this.item = ''; this.working = false; this.playing.clear(); this.state.active = false; this.state.status = status; this.update();
  }
}
