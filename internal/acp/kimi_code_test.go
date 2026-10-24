package acp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestKimiSessionConfigOptionsAndThinking(t *testing.T) {
	agent, conn := dialStdioTest(t, Handlers{})
	go func() {
		msg := agent.wait(t, "initialize")
		agent.reply(msg["id"], struct {
			ProtocolVersion int `json:"protocolVersion"`
		}{ProtocolVersion: 1})
		msg = agent.wait(t, "session/new")
		agent.reply(msg["id"], struct {
			SessionID     string `json:"sessionId"`
			ConfigOptions []struct {
				Type         string `json:"type"`
				ID           string `json:"id"`
				Category     string `json:"category"`
				CurrentValue string `json:"currentValue"`
				Options      []struct {
					Value string `json:"value"`
					Name  string `json:"name"`
				} `json:"options"`
			} `json:"configOptions"`
		}{SessionID: "kimi-session", ConfigOptions: []struct {
			Type         string `json:"type"`
			ID           string `json:"id"`
			Category     string `json:"category"`
			CurrentValue string `json:"currentValue"`
			Options      []struct {
				Value string `json:"value"`
				Name  string `json:"name"`
			} `json:"options"`
		}{
			{Type: "select", ID: "model", Category: "model", CurrentValue: "kimi-code/k3"},
			{Type: "select", ID: "thinking", Category: "thought_level", CurrentValue: "max"},
		}})
		msg = agent.wait(t, "session/set_config_option")
		raw, _ := json.Marshal(msg["params"])
		var params struct {
			SessionID string `json:"sessionId"`
			ConfigID  string `json:"configId"`
			Value     string `json:"value"`
		}
		if err := json.Unmarshal(raw, &params); err != nil || params.SessionID != "kimi-session" || params.ConfigID != "thinking" || params.Value != "high" {
			t.Fatalf("set_config_option params = %s, %v", raw, err)
		}
		agent.reply(msg["id"], struct{}{})
	}()
	if _, err := Handshake(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	a := ConnectedAgent(conn)
	setup, err := a.NewSession(context.Background(), "/repo", nil)
	if err != nil {
		t.Fatal(err)
	}
	if model, ok := setup.CurrentModel(); !ok || model != "kimi-code/k3" {
		t.Fatalf("model = %q, %v", model, ok)
	}
	if thinking, ok := setup.CurrentThinking(); !ok || thinking != "max" {
		t.Fatalf("thinking = %q, %v", thinking, ok)
	}
	if err := a.SetConfigOption(context.Background(), setup.SessionID, "thinking", "high"); err != nil {
		t.Fatal(err)
	}
}
