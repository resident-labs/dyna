import { openAudio, micError, type AudioHandle } from './audio.ts';
import { bytesToBase64, encodePCMFrame } from './pcm.ts';

export type CallState = { status: string; active: boolean; muted: boolean; transcript: { role: string; text: string }[] };
// An assistant transcript with item_id precedes each item's binary audio;
// the engine sends its final text later.
export class VoiceCall {
  state: CallState = { status: '', active: false, muted: false, transcript: [] };
  private audio?: AudioHandle;
  private socket?: WebSocket;
  private item = '';
  private generation = 0;
  private receiptTimer?: ReturnType<typeof setInterval>;
  constructor(private changed: (state: CallState) => void) {}
  private update() { this.changed({ ...this.state, transcript: [...this.state.transcript] }); }
  async start(room: string) {
    if (this.state.active) return;
    const generation = ++this.generation;
    this.state = { status: 'Opening microphone…', active: true, muted: false, transcript: [] }; this.update();
    try {
      const played = (item_id: string, audio_end_ms: number) => {
        if (this.socket?.readyState === WebSocket.OPEN) this.socket.send(JSON.stringify({ type: 'played', item_id, audio_end_ms: Math.floor(audio_end_ms) }));
      };
      const audio = await openAudio((frame, rate) => {
        if (!this.state.muted && this.socket?.readyState === WebSocket.OPEN && this.state.status !== 'Connecting…') this.socket.send(encodePCMFrame(frame, rate, 24000));
      }, { started: id => played(id, 0), progress: played, ended: played, cancelled: (id, _reason, ms) => played(id, ms) });
      if (generation !== this.generation) { audio.close(); return; }
      this.audio = audio;
      this.state.status = 'Connecting…'; this.update();
      const url = new URL('/v1/voice', location.href); url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'; url.searchParams.set('room', room);
      const ws = this.socket = new WebSocket(url); ws.binaryType = 'arraybuffer';
      let ticks = 0;
      this.receiptTimer = setInterval(() => {
        audio.reportPlayed();
        // Empty-item receipts keep a muted, silent page alive as well.
        if (++ticks % 60 === 0) played('', 0);
      }, 250);
      ws.onmessage = event => {
        if (event.data instanceof ArrayBuffer) {
          if (this.item) audio.play(bytesToBase64(new Uint8Array(event.data)), 24000, this.item);
          return;
        }
        const msg = JSON.parse(event.data);
        switch (msg.type) {
          case 'ready': this.state.status = 'Listening'; break;
          case 'speech_started': audio.stopPlayback('interrupted'); this.item = ''; this.state.status = 'Listening'; break;
          case 'transcript':
            if (msg.role === 'assistant' && msg.item_id) { this.item = msg.item_id; this.state.status = 'Speaking'; }
            if (msg.text) this.state.transcript.push({ role: msg.role, text: msg.text });
            break;
          case 'response_done': if (this.item) audio.done(this.item); this.state.status = 'Listening'; break;
          case 'closed': this.stop(msg.reason || 'Call ended'); break;
        }
        this.update();
      };
      ws.onerror = () => this.stop('Could not connect the call.');
      ws.onclose = () => { if (this.socket === ws) this.stop('Call ended'); };
    } catch (error) { if (generation === this.generation) this.stop(micError(error)); }
  }
  mute() { this.state.muted = !this.state.muted; this.update(); }
  stop(status = 'Call ended') {
    ++this.generation;
    clearInterval(this.receiptTimer); this.receiptTimer = undefined;
    this.audio?.close(); this.audio = undefined;
    const ws = this.socket; this.socket = undefined; ws?.close();
    this.item = ''; this.state.active = false; this.state.status = status; this.update();
  }
}
