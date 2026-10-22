package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"dyna-agent/internal/profile"
)

func openCodeAPIArgs(operation, id string, extra []string) []string {
	args := []string{"opencode", "api", operation, "--param", "sessionID=" + id}
	if operation == "session.compact" {
		args = append(args, "--data", "{}")
	}
	if server, ok := argumentValue(extra, "--server"); ok {
		args = append(args, "--server", server)
	}
	if hasArg(extra, "--standalone") {
		args = append(args, "--standalone")
	}
	return args
}

// OpenCode can replay a fast response without its step-finish usage event.
// Stored context gives the last completed model call's size in either case.
func openCodeContextTokens(ctx context.Context, p profile.Profile, cwd, id string, steering *steeringSupervisor, execution ExecutionOptions, admit func(context.Context) error) (int64, error) {
	if err := admit(ctx); err != nil {
		return 0, err
	}
	cmd, err := harnessCommand(ctx, p, cwd, openCodeAPIArgs("session.context", id, p.ExtraArgs), execution)
	if err != nil {
		return 0, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	wait, release, _, err := startGuarded(ctx, p, cmd, "", steering, execution)
	defer release()
	if err != nil {
		return 0, err
	}
	if err = wait(); err != nil {
		return 0, fmt.Errorf("read OpenCode context: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var body struct {
		Data []struct {
			Type   string `json:"type"`
			Tokens *struct {
				Input int64 `json:"input"`
				Cache struct {
					Read  int64 `json:"read"`
					Write int64 `json:"write"`
				} `json:"cache"`
			} `json:"tokens"`
		} `json:"data"`
	}
	if err = json.Unmarshal(stdout.Bytes(), &body); err != nil {
		return 0, fmt.Errorf("read OpenCode context: %w", err)
	}
	if body.Data == nil {
		return 0, fmt.Errorf("read OpenCode context: missing message array")
	}
	for i := len(body.Data) - 1; i >= 0; i-- {
		message := body.Data[i]
		if message.Type == "assistant" && message.Tokens != nil {
			return message.Tokens.Input + message.Tokens.Cache.Read + message.Tokens.Cache.Write, nil
		}
	}
	return 0, nil
}

func openCodeWaitResponse(output string) error {
	if strings.TrimSpace(output) != "" {
		return fmt.Errorf("OpenCode wait endpoint returned an unexpected response")
	}
	return nil
}

func openCodeCompactError(err error) error {
	reason, _, _ := strings.Cut(err.Error(), "\n")
	return fmt.Errorf("Dyna could not confirm OpenCode compaction; native automatic compaction stays enabled: %s", reason)
}
