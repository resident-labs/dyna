package realtime

// Ported from qwen-audio-agent server/src/voice/providers/ga-protocol.mjs.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// Protocol writes a provider's client events and maps its server events to
// Dyna's Events. Each method returns one JSON-encodable message.
type Protocol interface {
	SessionUpdate(session any) any
	AudioAppend(pcm []byte) any
	AudioCommit() any
	UserText(text string) any
	FunctionOutput(callID, output string) any
	// ResponseCreate asks for a response. A non-empty key marks a reply that
	// speaks a Dyna item: the provider echoes it on the response, and the
	// reply may not call tools.
	ResponseCreate(key string) any
	ResponseCancel() any
	// Truncate drops the part of an assistant audio item after audioEnd,
	// which the owner never heard.
	Truncate(itemID string, audioEnd time.Duration) any
	Decode(data []byte) (Event, error)
}

// Kind is the type of an Event.
type Kind int

const (
	Other           Kind = iota
	SessionReady         // the session update took effect
	SessionQueued        // the server let the client in: the session may be sent
	SpeechStarted        // the owner began to speak
	SpeechStopped        // the owner stopped speaking
	Committed            // the owner's turn is in the conversation, to be answered
	ResponseStarted      // a response began; ResponseID and Key are set
	AudioDelta           // Audio holds PCM16 of item ItemID
	AudioDone            // the audio of item ItemID is complete
	ResponseDone         // the response ended; Text is its status
	ToolCall             // the model called tool Name with Args
	OwnerPartial         // Text is the next part of the owner's words
	OwnerTranscript      // Text is the owner's finished words
	VoiceTranscript      // Text is what Voice said in item ItemID
	Error                // Text is the message, Code the provider's code
	VoiceDelta           // Text is the next part of what Voice says in item ItemID
	Interrupted          // the server stopped Voice's reply for the owner's speech
	Activity             // response ResponseID is alive, with nothing to say yet, such as thinking
)

// Event is one server event in Dyna's form. Type keeps the provider's name.
type Event struct {
	Kind       Kind
	Type       string
	ResponseID string
	ItemID     string
	Audio      []byte
	CallID     string
	Name       string
	Args       json.RawMessage
	Text       string
	Code       string
	// Key names the Dyna item a response speaks, from ResponseCreate.
	Key string
}

// GA speaks the GA (2025) form of the OpenAI Realtime protocol, which OpenAI
// and huggingface/speech-to-speech both use.
type GA struct{}

type msg map[string]any

func (GA) SessionUpdate(session any) any { return msg{"type": "session.update", "session": session} }

func (GA) AudioAppend(pcm []byte) any {
	return msg{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(pcm)}
}

func (GA) AudioCommit() any { return msg{"type": "input_audio_buffer.commit"} }

func (GA) UserText(text string) any {
	return msg{"type": "conversation.item.create", "item": msg{
		"type": "message", "role": "user",
		"content": []msg{{"type": "input_text", "text": text}},
	}}
}

// ImageProtocol is a Protocol that can add an image to the conversation. A
// provider that does so also sets Capabilities.ImageInput.
type ImageProtocol interface {
	// UserImage adds a PNG or JPEG image as a user item. It asks for no reply.
	UserImage(mime string, data []byte) any
}

// UserImage adds the image as an input_image item with a data URL.
func (GA) UserImage(mime string, data []byte) any {
	return msg{"type": "conversation.item.create", "item": msg{
		"type": "message", "role": "user",
		"content": []msg{{"type": "input_image", "image_url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)}},
	}}
}

// Context adds text without completing a turn or requesting speech.
func (g GA) Context(text string) any { return g.UserText(text) }

func (GA) FunctionOutput(callID, output string) any {
	return msg{"type": "conversation.item.create", "item": msg{
		"type": "function_call_output", "call_id": callID, "output": output,
	}}
}

func (GA) ResponseCreate(key string) any {
	if key == "" {
		return msg{"type": "response.create"}
	}
	return msg{"type": "response.create", "response": msg{"metadata": msg{speakKey: key}}}
}

// InstructedProtocol can put instructions on one response. A provider that
// does so also sets Capabilities.ResponseInstructions.
type InstructedProtocol interface {
	// ResponseInstructed asks for a response and sets its instructions.
	// A non-empty key marks a reply that speaks a Dyna item, as ResponseCreate does.
	ResponseInstructed(key, instructions string) any
}

// ResponseInstructed asks for one response and sets its instructions. Empty
// instructions are the same as ResponseCreate.
func (GA) ResponseInstructed(key, instructions string) any {
	response := msg{}
	if key != "" {
		response["metadata"] = msg{speakKey: key}
	}
	if instructions != "" {
		response["instructions"] = instructions
	}
	if len(response) == 0 {
		return msg{"type": "response.create"}
	}
	return msg{"type": "response.create", "response": response}
}

// speakKey is the response metadata field that names the Dyna item a reply speaks.
const speakKey = "dyna_speak"

func (GA) ResponseCancel() any { return msg{"type": "response.cancel"} }

func (GA) Truncate(itemID string, audioEnd time.Duration) any {
	return msg{"type": "conversation.item.truncate", "item_id": itemID, "content_index": 0, "audio_end_ms": audioEnd.Milliseconds()}
}

// gaEvent holds the fields of every GA server event Decode reads.
type gaEvent struct {
	Type       string `json:"type"`
	ResponseID string `json:"response_id"`
	ItemID     string `json:"item_id"`
	Delta      string `json:"delta"`
	Transcript string `json:"transcript"`
	CallID     string `json:"call_id"`
	Name       string `json:"name"`
	Arguments  string `json:"arguments"`
	Response   struct {
		ID       string            `json:"id"`
		Status   string            `json:"status"`
		Metadata map[string]string `json:"metadata"`
	} `json:"response"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (GA) Decode(data []byte) (Event, error) { return decodeEvent(data, nil) }

// decodeEvent maps a GA server event to Dyna's Event. names renames another
// dialect's event types to their GA names first; Event.Type keeps the
// provider's name.
func decodeEvent(data []byte, names map[string]string) (Event, error) {
	var e gaEvent
	if err := json.Unmarshal(data, &e); err != nil {
		return Event{}, fmt.Errorf("realtime: undecodable event: %w", err)
	}
	ev := Event{Type: e.Type, ResponseID: e.ResponseID, ItemID: e.ItemID}
	typ := e.Type
	if ga, ok := names[typ]; ok {
		typ = ga
	}
	switch typ {
	case "session.updated":
		ev.Kind = SessionReady
	case "input_audio_buffer.speech_started":
		ev.Kind = SpeechStarted
	case "input_audio_buffer.speech_stopped":
		ev.Kind = SpeechStopped
	case "input_audio_buffer.committed":
		ev.Kind = Committed
	case "response.created":
		ev.Kind, ev.ResponseID, ev.Key = ResponseStarted, e.Response.ID, e.Response.Metadata[speakKey]
	case "response.output_audio.delta":
		audio, err := base64.StdEncoding.DecodeString(e.Delta)
		if err != nil {
			return Event{}, fmt.Errorf("realtime: undecodable audio: %w", err)
		}
		ev.Kind, ev.Audio = AudioDelta, audio
	case "response.output_audio.done":
		ev.Kind = AudioDone
	case "response.done":
		ev.Kind, ev.ResponseID, ev.Text = ResponseDone, e.Response.ID, e.Response.Status
	case "response.function_call_arguments.done":
		ev.Kind, ev.CallID, ev.Name, ev.Args = ToolCall, e.CallID, e.Name, json.RawMessage(e.Arguments)
	case "conversation.item.input_audio_transcription.delta":
		ev.Kind, ev.Text = OwnerPartial, e.Delta
	case "conversation.item.input_audio_transcription.completed":
		ev.Kind, ev.Text = OwnerTranscript, e.Transcript
	case "response.output_audio_transcript.delta":
		ev.Kind, ev.Text = VoiceDelta, e.Delta
	case "response.output_audio_transcript.done":
		ev.Kind, ev.Text = VoiceTranscript, e.Transcript
	case "error":
		ev.Kind, ev.Text, ev.Code = Error, e.Error.Message, e.Error.Code
	}
	return ev, nil
}
