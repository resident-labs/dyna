package settings

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
)

const (
	// KeyVoiceProvider names the realtime voice provider a call uses.
	KeyVoiceProvider = "voice.provider"
	// KeyVoiceModel names the provider's model; empty means its default.
	KeyVoiceModel = "voice.model"
	// KeyVoiceURL is the provider's WebSocket address: required for a
	// self-hosted server, else it overrides the provider's own.
	KeyVoiceURL = "voice.url"
	// KeyVoiceDucking enables optional microphone-driven playback attenuation.
	KeyVoiceDucking = "voice.ducking"
	// KeyVoiceAgent lets agents use voice: dyna voice say and dyna voice
	// session. It is on by default; the owner turns it off to keep agents off
	// the voice provider's quota.
	KeyVoiceAgent = "voice.agent"
)

// VoiceProviders are the voice.provider values.
var VoiceProviders = []string{"openai", "speech-to-speech", "dashscope", "google-live", "doubao-seeduplex", "stepfun", "minicpm-o"}

// VoiceProviderNeeds is the line the owner sees under a voice.provider choice:
// the key variable or voice.url that provider needs. Every VoiceProviders name has one.
var VoiceProviderNeeds = map[string]string{
	"openai":           "Uses your ChatGPT sign-in.",
	"speech-to-speech": "Needs voice.url, the ws:// address of your server.",
	"dashscope":        "Needs DASHSCOPE_API_KEY.",
	"google-live":      "Needs GOOGLE_API_KEY or GEMINI_API_KEY.",
	"doubao-seeduplex": "Needs DOUBAO_API_KEY.",
	"stepfun":          "Needs STEPFUN_API_KEY.",
	"minicpm-o":        "Needs voice.url. It cannot use tools, so Voice cannot dispatch work.",
}

// loadVoice reads the known voice keys. Other voice keys stay in Extra.
func (v *Values) loadVoice(raw map[string]any) error {
	section, ok := raw["voice"]
	if !ok {
		return nil
	}
	values, ok := section.(map[string]any)
	if !ok {
		return fmt.Errorf("voice settings must be an object")
	}
	for _, key := range []string{KeyVoiceProvider, KeyVoiceModel, KeyVoiceURL, KeyVoiceDucking, KeyVoiceAgent} {
		if value, ok := values[strings.TrimPrefix(key, "voice.")]; ok {
			if key == KeyVoiceDucking || key == KeyVoiceAgent {
				if _, ok := value.(bool); !ok {
					return fmt.Errorf("%s must be a boolean", key)
				}
			}
			if err := v.setVoice(key, fmt.Sprint(value)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *Values) setVoice(key, value string) error {
	value = strings.TrimSpace(value)
	switch key {
	case KeyVoiceDucking, KeyVoiceAgent:
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		if key == KeyVoiceAgent {
			v.VoiceAgent = b
		} else {
			v.VoiceDucking = b
		}
	case KeyVoiceProvider:
		if !slices.Contains(VoiceProviders, value) {
			return fmt.Errorf("voice.provider must be one of %s", strings.Join(VoiceProviders, ", "))
		}
		v.VoiceProvider = value
	case KeyVoiceModel:
		v.VoiceModel = value
	case KeyVoiceURL:
		if u, err := url.Parse(value); value != "" && (err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "") {
			return fmt.Errorf("voice.url must be a ws:// or wss:// address")
		}
		v.VoiceURL = value
	}
	v.fromFile[key] = true
	return nil
}

func (v Values) saveVoice(raw map[string]any) {
	values := nested(raw, "voice")
	values["provider"], values["model"], values["url"] = v.VoiceProvider, v.VoiceModel, v.VoiceURL
	values["ducking"] = v.VoiceDucking
	values["agent"] = v.VoiceAgent
	raw["voice"] = values
}
