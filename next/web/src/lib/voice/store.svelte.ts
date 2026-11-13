import { VoiceCall, type CallState } from './call.ts';
// The call belongs to the page, not the mounted room view.
export const voice = $state({
  view: { status: '', active: false, muted: false, transcript: [] } as CallState,
  room: '', name: '', started: 0, ended: 0, visible: false,
});
export const voiceCall = new VoiceCall(view => { if (voice.view.active && !view.active) voice.ended = Date.now(); voice.view = view; });
export function startCall(room: string, name: string) {
  voice.room = room; voice.name = name; voice.started = Date.now(); voice.ended = 0; voice.visible = true;
  void voiceCall.start(room);
}
