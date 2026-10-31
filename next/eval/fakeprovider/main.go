// Command fakeprovider serves deterministic model responses for sandbox evaluations.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
)

type call struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type step struct {
	ID           string `json:"id"`
	Text         string `json:"text,omitempty"`
	Calls        []call `json:"calls,omitempty"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

type script struct {
	Models map[string][]step `json:"models"`
}

type server struct {
	mu      sync.Mutex
	script  script
	next    map[string]int
	records *json.Encoder
}

type request struct {
	Model  string          `json:"model"`
	Tools  json.RawMessage `json:"tools"`
	Stream bool            `json:"stream"`
}

type record struct {
	Fixture     string `json:"fixture"`
	Step        string `json:"step"`
	Request     int    `json:"request"`
	ToolsSHA256 string `json:"tools_sha256"`
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	var req request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil || !req.Stream {
		http.Error(w, "invalid streaming request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	steps, ok := s.script.Models[req.Model]
	index := s.next[req.Model]
	if !ok || index >= len(steps) {
		http.Error(w, "unknown or exhausted fixture", http.StatusBadRequest)
		return
	}
	current := steps[index]
	s.next[req.Model] = index + 1
	hash := sha256.Sum256(req.Tools)
	if err := s.records.Encode(record{req.Model, current.ID, index + 1, hex.EncodeToString(hash[:])}); err != nil {
		http.Error(w, "record failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	type function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	type toolCall struct {
		Index    int      `json:"index"`
		ID       string   `json:"id"`
		Type     string   `json:"type"`
		Function function `json:"function"`
	}
	type delta struct {
		Role      string     `json:"role,omitempty"`
		Content   string     `json:"content,omitempty"`
		ToolCalls []toolCall `json:"tool_calls,omitempty"`
	}
	type choice struct {
		Index        int     `json:"index"`
		Delta        delta   `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	}
	type usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	}
	type chunk struct {
		ID      string   `json:"id"`
		Object  string   `json:"object"`
		Model   string   `json:"model"`
		Choices []choice `json:"choices"`
		Usage   *usage   `json:"usage,omitempty"`
	}
	emit := func(c chunk) {
		b, _ := json.Marshal(c)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	base := chunk{ID: "fixture-" + current.ID, Object: "chat.completion.chunk", Model: req.Model}
	d := delta{Role: "assistant", Content: current.Text}
	for i, c := range current.Calls {
		d.ToolCalls = append(d.ToolCalls, toolCall{i, c.ID, "function", function{c.Name, string(c.Arguments)}})
	}
	base.Choices = []choice{{Index: 0, Delta: d}}
	emit(base)
	finish := "stop"
	if len(current.Calls) > 0 {
		finish = "tool_calls"
	}
	base.Choices = []choice{{Index: 0, FinishReason: &finish}}
	base.Usage = &usage{current.InputTokens, current.OutputTokens, current.InputTokens + current.OutputTokens}
	emit(base)
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func main() {
	fixture := flag.String("fixture", "", "path to fixture JSON")
	listen := flag.String("listen", "127.0.0.1:0", "loopback listen address")
	flag.Parse()
	data, err := os.ReadFile(*fixture)
	if err != nil {
		log.Fatal(err)
	}
	var scripts script
	if err := json.Unmarshal(data, &scripts); err != nil {
		log.Fatal(err)
	}
	addr, err := net.ResolveTCPAddr("tcp", *listen)
	if err != nil || addr.IP == nil || !addr.IP.IsLoopback() {
		log.Fatal("listen address must be a loopback IP")
	}
	listener, err := net.ListenTCP("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintln(os.Stderr, listener.Addr())
	log.Fatal(http.Serve(listener, &server{script: scripts, next: make(map[string]int), records: json.NewEncoder(os.Stdout)}))
}
