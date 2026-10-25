// Package workeroutput keeps this compatibility reader for old artifacts.
//
// A normalized tool event written before Dyna decoded the Muse result text
// directly carries its command, file path and exit code only inside the result
// text: the projection migration never persisted those fields, so removing the
// reader would lose them. It is used by transcript.Summarize and by
// ConvertLegacyEvents for retained envelopes only.
//
// TODO: legacy/shim remove later — fills omitted fields in old normalized Muse
// artifacts; remove after affected artifacts expire or migrate.
package workeroutput

import (
	"encoding/json"
	"regexp"
	"strings"
)

// UpgradeLegacyEvent recovers fields omitted by older normalized artifacts.
func UpgradeLegacyEvent(e Event) Event {
	if e.Kind != "tool" {
		return e
	}
	name := strings.ToLower(e.Name)
	var args map[string]any
	_ = json.Unmarshal([]byte(e.Input), &args)
	has := func(keys ...string) bool {
		for _, key := range keys {
			if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
				return true
			}
		}
		return false
	}
	switch name {
	case "command", "bash", "shell", "run_command", "run_terminal_cmd":
		if strings.TrimSpace(e.Input) != "" {
			return e
		}
	case "tool", "bash_input":
		if has("command") {
			return e
		}
	case "read", "read_file", "view_file":
		name = "read_file"
		if has("file_path", "path", "target_file", "AbsolutePath") {
			return e
		}
	case "write", "write_file", "write_to_file":
		name = "write_file"
		if has("file_path", "path", "target_file", "TargetFile") {
			return e
		}
	default:
		return e
	}
	input, output, exit, shell := legacyResultParts(name, e.Output)
	if input == "" {
		return e
	}
	e.Input = input
	if shell {
		e.Output = output
		if e.Exit == nil {
			e.Exit = exit
		}
	}
	return e
}

// legacyResultParts recovers a tool call's input, and for a shell call its exit
// code and bare output, from the result text an old envelope kept.
func legacyResultParts(name, text string) (input, output string, exit *int, shell bool) {
	output = text
	// A shell tool returns a JSON envelope that repeats the command. A finished
	// command carries terminal_status; a chunk of one still running carries
	// execution_state instead. Both carry chunk_id.
	var envelope struct {
		Command        string `json:"command"`
		Description    string `json:"description"`
		ExitCode       *int   `json:"exit_code"`
		TerminalStatus string `json:"terminal_status"`
		ExecutionState string `json:"execution_state"`
		ChunkID        string `json:"chunk_id"`
		Output         string `json:"output"`
	}
	if json.Unmarshal([]byte(text), &envelope) == nil && envelope.Command != "" &&
		(envelope.TerminalStatus != "" || envelope.ExecutionState != "" || envelope.ChunkID != "") {
		output, exit = envelope.Output, envelope.ExitCode
		if envelope.Command != "" {
			args := struct {
				Command     string `json:"command"`
				Description string `json:"description,omitempty"`
			}{envelope.Command, envelope.Description}
			if b, err := json.Marshal(args); err == nil {
				input = string(b)
			}
		}
		return input, output, exit, true
	}
	var path string
	switch name {
	case "read_file":
		// "Read text file `path`.\n<contents>"
		if m := legacyReadPath.FindStringSubmatch(text); m != nil {
			path = m[1]
		}
	case "write_file":
		// "wrote 12 bytes to path; note: …"
		if m := legacyWrotePath.FindStringSubmatch(text); m != nil {
			path = strings.TrimSpace(m[1])
		}
	}
	if path != "" {
		if b, err := json.Marshal(struct {
			Path string `json:"path"`
		}{path}); err == nil {
			input = string(b)
		}
	}
	return input, output, exit, false
}

var (
	legacyReadPath  = regexp.MustCompile("^Read [^`\n]*`([^`]+)`")
	legacyWrotePath = regexp.MustCompile(`(?m)^wrote [0-9]+ bytes to ([^;\n]+)`)
)
