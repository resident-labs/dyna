package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box/agent"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

// A conversation moves from a model that takes images to a text-only entry of the box's models.json, and the entry stands
// in for the catalog's model: the context window and the request's other settings are the model's, and an image the model
// reads reaches the endpoint as a placeholder.
func TestTextOnlyTransferOnPiG(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "ok"}}, []pigtest.Block{{Tool: "read", Input: `{"path":"pixel.png"}`}}, []pigtest.Block{{Text: "seen"}})
	// The first turn reports 300,000 tokens of context. That fits the catalog's 1,000,000-token window and not the
	// 272,000 of the OpenAI entry, nor a window the entry left unset.
	model.InputTokens = 300_000
	server := httptest.NewServer(model)
	t.Cleanup(server.Close)

	dir := pigtest.AgentDir(t, model, "")
	models := agent.Models(contract.TextOnlyModel{Provider: contract.ModelProviderAnthropic, Model: "text-opus"})
	anthropic := models.Providers["anthropic"]
	anthropic.BaseURL = server.URL
	models.Providers["anthropic"] = anthropic
	encoded, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	var pixel bytes.Buffer
	if err := png.Encode(&pixel, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "pixel.png"), pixel.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := pig.Open(context.Background(), pig.Config{Dir: work, AgentDir: dir, SessionDir: filepath.Join(dir, "sessions"), Model: contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "claude-opus-4-8", Thinking: contract.ThinkingXHigh}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := h.Start(ctx, "turn_a", []contract.TurnInput{{Delivery: 1, Text: "hello"}}, func() {}); err != nil {
		t.Fatal(err)
	}
	moved, err := h.TransferModel(ctx, "turn_b", contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "text-opus", Thinking: contract.ThinkingXHigh}, 0)
	if err != nil || moved.Path != "switched" {
		t.Fatalf("the transfer took path %q: %v", moved.Path, err)
	}
	if _, err := h.Start(ctx, "turn_b", []contract.TurnInput{{Delivery: 2, Text: "read pixel.png"}}, func() {}); err != nil {
		t.Fatal(err)
	}

	requests := model.Requests()
	if len(requests) != 3 {
		t.Fatalf("%d requests", len(requests))
	}
	var sent strings.Builder
	for _, message := range requests[2].Messages {
		sent.Write(message)
	}
	withResult := sent.String()
	if !strings.Contains(withResult, `"tool_result"`) || strings.Contains(withResult, `"type":"image"`) || strings.Contains(withResult, "iVBOR") {
		t.Fatalf("the image went to a text-only model: %s", withResult)
	}
	if !strings.Contains(strings.ToLower(withResult), "image") {
		t.Fatalf("the image left no placeholder: %s", withResult)
	}
	if !reflect.DeepEqual(requests[1].OutputConfig, requests[0].OutputConfig) {
		t.Fatalf("settings differ from the catalog model's: %s, then %s", requests[0].OutputConfig, requests[1].OutputConfig)
	}
}
