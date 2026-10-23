package acp

import (
	"encoding/json"
)

// McpServer is one session-scoped stdio or HTTP MCP definition.
type McpServer struct {
	Name    string        `json:"name"`
	Command string        `json:"command"`
	Args    []string      `json:"args"`
	Env     []EnvVariable `json:"env"`
	URL     string        `json:"-"`
}

// MarshalJSON emits the ACP transport union with required empty arrays.
func (s McpServer) MarshalJSON() ([]byte, error) {
	if s.URL != "" {
		return json.Marshal(struct {
			Name    string        `json:"name"`
			Type    string        `json:"type"`
			URL     string        `json:"url"`
			Headers []EnvVariable `json:"headers"`
		}{s.Name, "http", s.URL, []EnvVariable{}})
	}
	args, env := s.Args, s.Env
	if args == nil {
		args = []string{}
	}
	if env == nil {
		env = []EnvVariable{}
	}
	return json.Marshal(struct {
		Name    string        `json:"name"`
		Command string        `json:"command"`
		Args    []string      `json:"args"`
		Env     []EnvVariable `json:"env"`
	}{s.Name, s.Command, args, env})
}

// EnvVariable is one MCP server environment entry.
type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
