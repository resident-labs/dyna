package images

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type Host interface {
	Build(context.Context, string, string, []byte) error
}
type Recorder interface {
	Record(context.Context, contract.RoomID, contract.EventBody) error
}
type buildLog interface {
	error
	BuildLog() string
}

type Builder struct {
	Recorder Recorder
	Now      func() time.Time
	mu       sync.Mutex
	locks    map[contract.HostName]*sync.Mutex
}

// Build serializes a host's build checks and records their outcome.
func (b *Builder) Build(ctx context.Context, host Host, room contract.RoomID, hostName contract.HostName, repo contract.RepoName, name, base string, tree TreeID, data []byte) (string, error) {
	tag := Tag(name, base, tree)
	b.mu.Lock()
	if b.locks == nil {
		b.locks = make(map[contract.HostName]*sync.Mutex)
	}
	lock := b.locks[hostName]
	if lock == nil {
		lock = &sync.Mutex{}
		b.locks[hostName] = lock
	}
	b.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	start := b.Now()
	err := host.Build(ctx, tag, base, data)
	var body contract.EventBody
	if err == nil {
		body = contract.ImageBuilt{Host: hostName, Repo: repo, Name: name, Tag: tag, Seconds: b.Now().Sub(start).Seconds()}
	} else {
		reason, _, _ := strings.Cut(err.Error(), "\n")
		var failure buildLog
		var tail string
		if errors.As(err, &failure) {
			tail = failure.BuildLog()
		}
		body = contract.ImageFailed{Host: hostName, Repo: repo, Name: name, Tag: tag, Reason: reason, LogTail: tail}
		err = errors.New(reason)
	}
	if recordErr := b.Recorder.Record(context.WithoutCancel(ctx), room, body); recordErr != nil {
		return "", recordErr
	}
	return tag, err
}
