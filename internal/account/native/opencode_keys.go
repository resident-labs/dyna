package native

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// ProviderKey names one provider of a harness. It never carries a key.
type ProviderKey struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Auth says how the provider is signed in: "api_key" or "oauth". It is
	// empty for a catalog choice.
	Auth string `json:"auth,omitempty"`
}

// OpenCodeKeys reads, adds and removes API-key providers in a prepared
// OpenCode scope. The host is the one used for that scope's account commands.
type OpenCodeKeys struct{ m *openCodeManager }

// OpenCodeKeysOf returns the key operations of an OpenCode manager.
func OpenCodeKeysOf(m Manager) (OpenCodeKeys, bool) {
	oc, ok := m.(*openCodeManager)
	return OpenCodeKeys{oc}, ok
}

func (k OpenCodeKeys) args(args ...string) []string {
	if !k.m.host.OpenCodeService {
		args = append(args, "--standalone")
	}
	return args
}

// Choices lists the providers the installed OpenCode can take a key for.
func (k OpenCodeKeys) Choices(ctx context.Context) ([]ProviderKey, error) {
	s, err := k.m.scope()
	if err != nil {
		return nil, err
	}
	raw, err := k.m.run(ctx, s, k.args("api", "GET", "/api/integration")...)
	if err != nil {
		return nil, err
	}
	var reply struct {
		Data []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Methods []struct {
				Type string `json:"type"`
			} `json:"methods"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &reply) != nil {
		return nil, ErrSchema
	}
	choices := []ProviderKey{}
	for _, row := range reply.Data {
		for _, method := range row.Methods {
			if method.Type == "key" && openCodeRefID.MatchString(row.ID) {
				choices = append(choices, ProviderKey{ID: row.ID, Name: row.Name})
				break
			}
		}
	}
	return choices, nil
}

// keyCredentials maps each provider that holds an API key to its credential IDs.
func (k OpenCodeKeys) keyCredentials(ctx context.Context, s openCodeScope) (map[string][]string, map[string]string, error) {
	raw, err := k.m.run(ctx, s, k.args("auth", "list", "--format", "json")...)
	if err != nil {
		return nil, nil, err
	}
	var rows []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Connections []struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			Method string `json:"method"`
		} `json:"connections"`
	}
	if json.Unmarshal(raw, &rows) != nil {
		return nil, nil, ErrSchema
	}
	ids, names := map[string][]string{}, map[string]string{}
	for _, row := range rows {
		for _, c := range row.Connections {
			if c.Type == "credential" && c.Method == "key" && openCodeRefID.MatchString(row.ID) && openCodeRefID.MatchString(c.ID) {
				ids[row.ID] = append(ids[row.ID], c.ID)
				names[row.ID] = row.Name
			}
		}
	}
	return ids, names, nil
}

// List returns the providers that hold an API key, by name only.
func (k OpenCodeKeys) List(ctx context.Context) ([]ProviderKey, error) {
	s, err := k.m.scope()
	if err != nil {
		return nil, err
	}
	ids, names, err := k.keyCredentials(ctx, s)
	if err != nil {
		return nil, err
	}
	list := []ProviderKey{}
	for id := range ids {
		list = append(list, ProviderKey{ID: id, Name: names[id]})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, nil
}

// Add stores key for provider with `opencode auth import`. The key travels on
// stdin only.
func (k OpenCodeKeys) Add(ctx context.Context, provider, key string) error {
	s, err := k.m.scope()
	if err != nil {
		return err
	}
	if !openCodeRefID.MatchString(provider) {
		return ErrUnsupported
	}
	var id [8]byte
	if _, err = rand.Read(id[:]); err != nil {
		return err
	}
	credential := []map[string]any{{
		"id": "cred_" + hex.EncodeToString(id[:]), "integrationID": provider, "label": "API key", "active": true,
		"value": map[string]string{"type": "key", "key": key},
	}}
	body, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	_, err = k.m.runInput(ctx, s, bytes.NewReader(body), k.args("auth", "import")...)
	return err
}

// Remove deletes every API-key credential of provider. Other logins stay.
func (k OpenCodeKeys) Remove(ctx context.Context, provider string) error {
	s, err := k.m.scope()
	if err != nil {
		return err
	}
	ids, _, err := k.keyCredentials(ctx, s)
	if err != nil {
		return err
	}
	for _, id := range ids[provider] {
		if _, err = k.m.run(ctx, s, k.args("auth", "logout", provider, id)...); err != nil {
			return err
		}
	}
	return nil
}
