package realtime

// Ported from qwen-audio-agent
// server/src/voice/providers/openai-compatible-protocol.mjs (Apache-2.0).

// Beta speaks the beta (2024) form of the OpenAI Realtime protocol, which
// DashScope and StepFun use. It differs from GA in its audio event names,
// and in a response.create that names its modalities and has no metadata:
// a reply is matched to the request in the order sent, which the call does
// because it creates one response at a time.
type Beta struct {
	GA
	// Modalities are the response modalities, such as text and audio.
	Modalities []string
}

// betaNames are the beta server events that GA names otherwise.
var betaNames = map[string]string{
	"response.audio.delta":            "response.output_audio.delta",
	"response.audio.done":             "response.output_audio.done",
	"response.audio_transcript.delta": "response.output_audio_transcript.delta",
	"response.audio_transcript.done":  "response.output_audio_transcript.done",
}

func (b Beta) ResponseCreate(string) any {
	return msg{"type": "response.create", "response": msg{"modalities": b.Modalities}}
}

func (Beta) Decode(data []byte) (Event, error) { return decodeEvent(data, betaNames) }
