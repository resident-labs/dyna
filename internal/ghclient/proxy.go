package ghclient

import (
	"context"
	"fmt"
	"os"
)

var sessionCredential credentialSource = engineCredential

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
	binary := os.Getenv(BinaryEnv)
	if !isExecutable(binary) {
		return fmt.Errorf("the GitHub guard has no real gh binary")
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	if ref := os.Getenv("DYNA_ROOM_ID"); ref != "" {
		ctx = WithRoom(ctx, ref)
	}
	cmd := Command(ctx, binary, dir, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return runWithAccount(ctx, cmd, nil, sessionCredential)
}
