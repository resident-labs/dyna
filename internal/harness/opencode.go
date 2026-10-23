package harness

import (
	"context"
	"fmt"
	"strings"

	"dyna-agent/internal/profile"
)

// OpenCode can replay a fast response without its step-finish usage event.
// Stored context gives the last completed model call's size in either case.
func openCodeContextTokens(ctx context.Context, p profile.Profile, cwd, id string, steering *steeringSupervisor, execution ExecutionOptions, admit func(context.Context) error) (int64, error) {
	if err := admit(ctx); err != nil {
		return 0, err
	}
	key, err := sessionKey(p, execution)
	if err != nil {
		return 0, err
	}
	spec := commandSpec{argv: []string{"opencode", "serve"}, openCode: true, openCodeKey: key, openCodeSession: id}
	sess, err := openCodeSessionProcessFor(ctx, p, cwd, spec, execution)
	if err != nil {
		return 0, err
	}
	defer sess.detach()

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
	if err = sess.request(ctx, "GET", sess.path("/context"), nil, &body); err != nil {
		return 0, err
	}
	sess.idle = true

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

func runOpenCodeCompact(ctx context.Context, p profile.Profile, cwd string, spec commandSpec, execution ExecutionOptions, admit func(context.Context) error) error {
	if err := admit(ctx); err != nil {
		return err
	}
	sess, err := openCodeSessionProcessFor(ctx, p, cwd, spec, execution)
	if err != nil {
		return err
	}
	defer sess.detach()
	if spec.openCodeOperation == "wait" {
		var body strings.Builder
		// Wait has an empty success body. A changed endpoint must fail before compaction.
		if err = sess.request(ctx, "POST", "/api/experimental/session/"+sess.session+"/wait", nil, &body); err != nil {
			return err
		}
	} else {
		if err = sess.request(ctx, "POST", sess.path("/compact"), map[string]string{"delivery": "steer"}, nil); err != nil {
			return err
		}
	}
	sess.idle = true
	return nil
}
