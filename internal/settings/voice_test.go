package settings

import (
	"path/filepath"
	"testing"
)

func TestVoiceSettingsDefaultSetSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v, err := Load(path)
	if err != nil || v.VoiceProvider != "openai" || v.VoiceURL != "" || v.VoiceDucking {
		t.Fatalf("defaults = %q %q, %v", v.VoiceProvider, v.VoiceURL, err)
	}
	for key, value := range map[string]string{KeyVoiceDucking: "true", KeyVoiceProvider: "speech-to-speech", KeyVoiceModel: "m1", KeyVoiceURL: "ws://box:8000/v1/realtime"} {
		if err := v.Set(key, value); err != nil {
			t.Fatal(key, err)
		}
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || !got.VoiceDucking || got.VoiceProvider != "speech-to-speech" || got.VoiceModel != "m1" || got.VoiceURL != "ws://box:8000/v1/realtime" || got.Source(KeyVoiceProvider) != SourceFile {
		t.Fatalf("loaded %q %q, %v", got.VoiceProvider, got.VoiceURL, err)
	}
	for key, bad := range map[string]string{KeyVoiceDucking: "maybe", KeyVoiceProvider: "qwen", KeyVoiceURL: "http://box:8000"} {
		if err := v.Set(key, bad); err == nil {
			t.Errorf("%s accepted %q", key, bad)
		}
	}
}
