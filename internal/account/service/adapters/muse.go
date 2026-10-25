package adapters

import (
	"context"
	"encoding/json"
	"os"
	"strings"
)

// MSP wire and projection evidence (experimental schema, inspected 2026-10-17):
// https://meta-models.github.io/muse-code-sdk/next/guides/msp-wire/
// https://meta-models.github.io/muse-code-sdk/next/generated/msp/types/clientcapabilities/
// https://meta-models.github.io/muse-code-sdk/next/generated/msp/types/accountstate/
// https://meta-models.github.io/muse-code-sdk/next/generated/msp/types/subscriptionusage/
// T3 packages/provider-muse/src/server/sdk.ts establishes the read-only flags.
func openMuseRPC(ctx context.Context, executable string, base []string) (*discoveryRPC, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if base == nil {
		base = os.Environ()
	}
	env := []string{}
	for _, value := range base {
		name, _, _ := strings.Cut(value, "=")
		if !strings.EqualFold(name, "MUSE_NO_AUTO_UPDATE") {
			env = append(env, value)
		}
	}
	env = append(env, "MUSE_NO_AUTO_UPDATE=1")
	rpc, err := openDiscoveryRPC(ctx, executable, env, "serve", "--disable-shell", "--disable-write", "--no-session-log")
	if err != nil {
		return nil, err
	}
	rpc.wireVersion = "2.0"
	var initialized struct {
		Schema struct {
			Version int `json:"version"`
		} `json:"schema"`
		Experimental bool      `json:"experimentalApi"`
		Granted      *[]string `json:"grantedCapabilities"`
	}
	params := map[string]any{"clientInfo": map[string]string{"name": "dyna_account_discovery", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true, "requestedCapabilities": []string{}, "userInputDialogs": false}}
	if err = rpc.call("initialize", params, &initialized); err != nil {
		rpc.close()
		return nil, err
	}
	// AccountState is experimental, with no separate capability registry grant.
	// The host must echo its opt-in. Baseline model/list only needs envelope v1.
	if initialized.Schema.Version != 1 || initialized.Granted == nil {
		rpc.close()
		return nil, ErrUnsupported
	}
	if json.NewEncoder(rpc.input).Encode(map[string]any{"jsonrpc": "2.0", "method": "initialized"}) != nil {
		rpc.close()
		return nil, ErrProbe
	}
	return rpc, nil
}

func modelsMuse(ctx context.Context, executable string, env []string) ([]Model, error) {
	rpc, err := openMuseRPC(ctx, executable, env)
	if err != nil {
		return nil, err
	}
	defer rpc.close()
	var response struct {
		Provider string `json:"providerId"`
		Source   string `json:"source"`
		Models   *[]struct {
			ID       string `json:"modelId"`
			Label    string `json:"displayLabel"`
			Provider string `json:"providerId"`
			Context  int64  `json:"contextLimit"`
			Output   int64  `json:"outputLimit"`
			// variants lists every effort tier; reasoningEffortVariants only
			// describes some of them.
			Efforts       []string `json:"variants"`
			DefaultEffort string   `json:"defaultReasoningEffort"`
		} `json:"models"`
	}
	if err = rpc.call("model/list", map[string]any{}, &response); err != nil {
		return nil, err
	}
	if response.Provider == "" || response.Models == nil {
		return nil, ErrProbe
	}
	out := []Model{}
	seen := map[string]bool{}
	for _, row := range *response.Models {
		if row.ID == "" || row.Provider == "" || row.Provider != response.Provider || row.Context < 0 || row.Output < 0 {
			return nil, ErrProbe
		}
		key := row.Provider + "\x00" + row.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		name := row.Label
		if name == "" {
			name = row.ID
		}
		source := "Muse MSP model/list; account access unverified"
		if response.Source != "" {
			source += "; catalog source: " + response.Source
		}
		m := Model{ID: row.ID, Name: name, Provider: row.Provider, Context: row.Context, Output: row.Output, Source: source}
		for _, e := range row.Efforts {
			if e != "" && e != "unknown" {
				m.Efforts = append(m.Efforts, Effort{ID: e, Default: e == row.DefaultEffort, Args: []string{"--reasoning-effort", e}})
			}
		}
		out = append(out, m)
	}
	return out, nil
}
