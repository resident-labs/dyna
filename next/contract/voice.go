package contract

const (
	MethodVoiceStatus Method = "voice.status"
	MethodVoiceStop   Method = "voice.stop"
)

type VoiceStatusRequest struct{}
type VoiceStatusResponse struct {
	Room RoomID `json:"room"`
}
type VoiceStopRequest struct{}
type VoiceStopResponse struct{}
