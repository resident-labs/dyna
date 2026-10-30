package runtime

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestDormancyKeepsWakeDuringSlowStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan contract.Event)
	kicks := dormancyKicks(ctx, func() (<-chan contract.Event, func()) { return updates, func() {} })
	stopping, release, woke := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		<-kicks
		close(stopping)
		<-release // The fake host stop is still in progress.
		<-kicks
		close(woke)
	}()
	updates <- contract.Event{}
	<-stopping
	for i := 0; i < 129; i++ {
		updates <- contract.Event{}
	}
	updates <- contract.Event{} // A waking delivery after the burst.
	close(release)
	<-woke
}
