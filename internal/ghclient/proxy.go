package ghclient

import (
	"context"
	"fmt"
	"os"
)

// SessionCommand runs the gh multicall guard through the shared room policy.
func SessionCommand(ctx context.Context, args []string) error {
	mode := GuardMode(os.Getenv(ReadOnlyEnv))
	switch mode {
	case GuardReadOnly:
	case GuardWorker:
		if err := checkWorkerPolicy(args); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid GitHub guard mode")
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	if ref := os.Getenv("DYNA_ROOM_ID"); ref != "" {
		ctx = WithRoom(ctx, ref)
	}
	input, err := sessionInput(os.Stdin)
	if err != nil {
		return err
	}
	result, err := sessionOperationCall(ctx, "GitHubCommand", CommandRequest{Dir: dir, Args: args, Input: input})
	if err != nil {
		return err
	}
	return finishSessionOperation(result)
}
