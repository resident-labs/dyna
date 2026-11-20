package cliproxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestDynaAntigravityRegistersAndRoutesNamespacedModels(t *testing.T) {
	ctx := t.Context()
	const account = "dyna-antigravity-test"
	const opus = "claude-opus-5-5-high"
	const neon = "gemini-4-neon-xhigh"
	seen := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case antigravityModelsPath:
			_, _ = fmt.Fprintf(w, `{"models":{%q:{},%q:{},"not-in-the-catalog":{}}}`, opus, neon)
		case "/v1internal:generateContent", "/v1internal:streamGenerateContent":
			var request struct {
				Model   string
				Request json.RawMessage
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.Model == neon {
				if gjson.GetBytes(request.Request, "generationConfig.thinkingConfig.thinkingLevel").String() != "xhigh" {
					t.Error("Neon lost its xhigh thinking level")
				}
				if gjson.GetBytes(request.Request, "tools.0.functionDeclarations.0.name").String() != "codemode" {
					t.Error("Neon lost the Messages API tool declaration")
				}
			}
			seen <- request.Model
			const response = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}]}}`
			if r.URL.Path == "/v1internal:streamGenerateContent" {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: %s\n\n", response)
			} else {
				_, _ = fmt.Fprint(w, response)
			}
		default:
			t.Errorf("unexpected Antigravity request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	cfg := &config.Config{}
	cfg.ForceModelPrefix = true
	cfg.Models.Catalog = "embed"
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: cfg, coreManager: manager}
	auth := antigravityTestAuth(account, server.URL)
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	if err := svc.RegisterDynaCredential(ctx, auth); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(account) })
	if err := svc.ActivateDynaCredential(ctx, account, ""); err != nil {
		t.Fatal(err)
	}
	svc.WaitAntigravityProbes()
	credentials := svc.DynaCredentials()
	if len(credentials) != 1 || !slices.Equal(credentials[0].Models, []string{"antigravity/" + opus, "antigravity/" + neon}) {
		t.Fatalf("account catalog = %+v", credentials)
	}
	for _, upstream := range []string{opus, neon} {
		id := "antigravity/" + upstream
		effort := "high"
		if upstream == neon {
			effort = "xhigh"
		}
		payload := fmt.Appendf(nil, `{"model":%q,"max_tokens":16000,"thinking":{"type":"adaptive"},"output_config":{"effort":%q},"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"codemode","input_schema":{"type":"object","properties":{"code":{"type":"string"}}}}]}`, id, effort)
		response, err := manager.Execute(ctx, []string{"antigravity"}, executor.Request{Model: id, Payload: payload}, executor.Options{SourceFormat: translator.FormatClaude})
		if err != nil || !strings.Contains(string(response.Payload), "hello") {
			t.Fatalf("%s did not run through Antigravity: %v", id, err)
		}
		select {
		case model := <-seen:
			if model != upstream {
				t.Fatalf("upstream model = %q, want %q", model, upstream)
			}
		default:
			t.Fatal("no request reached Antigravity")
		}
	}
}
