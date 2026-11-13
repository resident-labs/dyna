package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"gopkg.in/yaml.v3"
)

// TestLiveProxyAudio is deliberately opt-in: it consumes the selected ChatGPT
// account's quota. Point it at the private proxy, NOT the local model forwarder.
func TestLiveProxyAudio(t *testing.T) {
	if os.Getenv("DYNA_VOICE_LIVE") != "1" {
		t.Skip("set DYNA_VOICE_LIVE=1 to spend live account quota")
	}
	socket, config, accounts := os.Getenv("DYNA_VOICE_PROXY_SOCKET"), os.Getenv("DYNA_VOICE_PROXY_CONFIG"), os.Getenv("DYNA_VOICE_AUTH_IDS")
	if socket == "" || config == "" || accounts == "" {
		t.Fatal("DYNA_VOICE_PROXY_SOCKET, DYNA_VOICE_PROXY_CONFIG and DYNA_VOICE_AUTH_IDS are required")
	}
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Keys []string `yaml:"api-keys"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Keys) != 1 {
		t.Fatal("expected one private proxy key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	conn, err := DialProxy(ctx, socket, cfg.Keys[0], strings.Split(accounts, ","))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "probe complete")
	if err := writeJSON(ctx, conn, sessionUpdate()); err != nil {
		t.Fatal(err)
	}
	requested := false
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var event providerEvent
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		switch event.Type {
		case "error":
			t.Fatalf("provider: %s", event.Error)
		case "session.updated":
			if requested {
				continue
			}
			requested = true
			if err := writeJSON(ctx, conn, userText("This is an audio connectivity probe. Say hello briefly. Do not call tools.")); err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(ctx, conn, message{"type": "response.create"}); err != nil {
				t.Fatal(err)
			}
		case "response.output_audio.delta":
			pcm, err := base64.StdEncoding.DecodeString(event.Delta)
			if err != nil {
				t.Fatal(err)
			}
			if len(pcm) > 0 {
				t.Log("received speech audio through private proxy")
				return
			}
		}
	}
}
