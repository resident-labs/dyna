package database

import (
	"context"
	"errors"
	"testing"
)

// A wal-index contended past SQLite's own retries answers SQLITE_PROTOCOL
// instead of SQLITE_BUSY. The retry loop must treat it the same: retry it
// with the short bounded backoff, count it for Health, and, when the deadline
// ends first, return an error that keeps both the cancellation and the last
// SQLite answer. The code extraction is replaced, because such contention
// cannot be staged without real IO pressure (D-448).
func TestRetryAnswersProtocolLikeBusy(t *testing.T) {
	codes := map[error]int{}
	oldCode := primaryCode
	primaryCode = func(err error) int { return codes[err] }
	t.Cleanup(func() { primaryCode = oldCode })
	protocol := errors.New("locking protocol (15)")
	codes[protocol] = 15

	// One SQLITE_PROTOCOL answer, then success: the read is retried.
	retried := make(chan struct{}, 1)
	oldRetry := busyRetry
	busyRetry = func() {
		select {
		case retried <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { busyRetry = oldRetry })
	attempts, before := 0, BusyRetries()
	err := Retry(context.Background(), func() error {
		attempts++
		if attempts == 1 {
			return protocol
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("retry after one SQLITE_PROTOCOL = %v after %d attempts, want success on the second attempt", err, attempts)
	}
	select {
	case <-retried:
		if BusyRetries() <= before {
			t.Fatal("a retried SQLITE_PROTOCOL was not counted for Health")
		}
	default:
		t.Fatal("SQLITE_PROTOCOL was returned instead of retried")
	}

	// A deadline that already ended keeps the first attempt, and the error
	// keeps both the cancellation and the final SQLite answer.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = Retry(ctx, func() error { return protocol })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retry on an ended context = %v, want it to report the cancellation", err)
	}
	if !errors.Is(err, protocol) {
		t.Fatalf("retry on an ended context = %v, want it to keep the last SQLITE_PROTOCOL", err)
	}
}
