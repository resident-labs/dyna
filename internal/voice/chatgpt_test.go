package voice

import (
	"fmt"
	"strings"
	"testing"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/harness/runtime"
)

// A missing Pi copy is reported as that, even though the reader also reports the
// grant as expired: the owner cannot fix it by signing in again.
func TestChatGPTErrorNamesAMissingPiCopy(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{name: "missing Pi", err: fmt.Errorf("%w: %w", runtime.ErrGrantExpired, runtime.ErrPiMissing), want: "copy of Pi is not installed"},
		{name: "expired", err: fmt.Errorf("%w: %w", runtime.ErrGrantExpired, native.ErrInvalidGrant), want: "expired and could not be renewed"},
		{name: "not signed in", err: native.ErrNotSignedIn, want: "sign in to a ChatGPT account"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := chatGPTError(test.err)
			if got == nil || !strings.Contains(got.Error(), test.want) {
				t.Fatalf("error = %v, want %q", got, test.want)
			}
		})
	}
}
