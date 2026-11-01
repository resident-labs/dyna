package agent

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed crew_build.sh
var buildShim string

// buildTools are the commands a crew's box puts behind the build lock.
var buildTools = []string{"cargo", "go", "npm", "npx", "pnpm", "yarn", "bun", "make", "ninja", "cmake", "gradle", "mvn", "bazel"}

// buildLockHold is how long a command may keep others waiting. A server started with npm run dev looks like a build, and a
// job outlives its worker, so without this one forgotten job would hold every build for good.
const buildLockHold = time.Hour

// buildLock lets one build run in a box at a time: run() takes it in-process and the shims take it over a local connection.
// It also adds up the time anything has waited, which a worker's timeout does not count.
type buildLock struct {
	slot     chan struct{}
	listener net.Listener
	hold     time.Duration

	mu      sync.Mutex
	waiters int
	since   time.Time
	total   time.Duration
}

// newBuildLock listens for the shims and writes one for each tool the box has, into folder.
func newBuildLock(folder string, hold time.Duration) (*buildLock, error) {
	if err := os.MkdirAll(folder, 0700); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	script := strings.ReplaceAll(buildShim, "__DYNA_BUILD_LOCK_PORT__", port)
	for _, tool := range buildTools {
		if _, err := exec.LookPath(tool); err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(folder, tool), []byte(script), 0700); err != nil {
			_ = listener.Close()
			return nil, err
		}
	}
	lock := &buildLock{slot: make(chan struct{}, 1), listener: listener, hold: hold}
	go lock.serve()
	return lock, nil
}

func (l *buildLock) serve() {
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			return
		}
		go l.answer(conn)
	}
}

// answer tells one shim "wait" if the lock is taken, and "go" once it has it. The lock is the shim's until it hangs up.
func (l *buildLock) answer(conn net.Conn) {
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		cancel()
	}()
	release, err := l.acquire(ctx, func() { fmt.Fprintln(conn, "wait") })
	if err != nil {
		return
	}
	defer release()
	fmt.Fprintln(conn, "go")
	select {
	case <-ctx.Done():
	case <-time.After(l.hold):
	}
}

func (l *buildLock) acquire(ctx context.Context, queued func()) (func(), error) {
	release := func() { <-l.slot }
	select {
	case l.slot <- struct{}{}:
		return release, nil
	default:
	}
	if queued != nil {
		queued()
	}
	l.queue(1)
	defer l.queue(-1)
	select {
	case l.slot <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *buildLock) queue(change int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.waiters > 0 {
		l.total += time.Since(l.since)
	}
	l.since = time.Now()
	l.waiters += change
}

// waited is how long at least one command has been waiting, up to now.
func (l *buildLock) waited() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.waiters > 0 {
		return l.total + time.Since(l.since)
	}
	return l.total
}

// timeout ends ctx with DeadlineExceeded as its cause after limit, leaving out the time the lock had a queue. Any worker may
// be the one waiting, so all of them get that time, and none can fail for its place in the queue.
func (l *buildLock) timeout(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	started, waited := time.Now(), l.waited()
	go func() {
		for {
			left := limit - time.Since(started) + l.waited() - waited
			if left <= 0 {
				cancel(context.DeadlineExceeded)
				return
			}
			select {
			case <-time.After(left):
			case <-ctx.Done():
				return
			}
		}
	}()
	return ctx, func() { cancel(nil) }
}
