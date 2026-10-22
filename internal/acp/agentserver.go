package acp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
)

// RandomServerSecret makes the token a spawned agent server requires.
func RandomServerSecret() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ReserveLocalPort picks a free loopback port the same way the OpenCode
// service does: bind, close, and let the caller race for it.
func ReserveLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

// AgentURL builds the serve endpoint URL for a port and secret.
func AgentURL(port int, secret string) string {
	return (&url.URL{Scheme: "ws", Host: fmt.Sprintf("127.0.0.1:%d", port), Path: "/ws", RawQuery: "server-key=" + secret}).String()
}

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
