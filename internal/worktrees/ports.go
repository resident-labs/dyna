package worktrees

import (
	"context"
	"fmt"
	"hash/fnv"
	"net"
	"path/filepath"
	"strconv"
)

const (
	portMin   = 20000
	portSlots = 1000 // blocks in the 20000-29999 range
	blockSize = 10
)

// blockFor returns the first port of the block that path prefers.
func blockFor(path string) int {
	h := fnv.New32a()
	h.Write([]byte(path))
	return portMin + blockSize*int(h.Sum32()%portSlots)
}

// allocatePort returns the first port of a free block for path. A block is
// free when no record holds its first port and isFree accepts all its ports.
// It tries the preferred block first, then the following blocks, wrapping at
// the end of the range.
func allocatePort(path string, records []Record, isFree func(int) bool) (int, error) {
	taken := map[int]bool{}
	for _, rec := range records {
		taken[rec.Port] = true
	}
	first := blockFor(path)
	for i := 0; i < portSlots; i++ {
		start := portMin + (first-portMin+i*blockSize)%(portSlots*blockSize)
		if taken[start] || !blockFree(start, isFree) {
			continue
		}
		return start, nil
	}
	return 0, fmt.Errorf("no free port block of %d ports in %d-%d; run dyna git worktrees gc", blockSize, portMin, portMin+portSlots*blockSize-1)
}

func blockFree(start int, isFree func(int) bool) bool {
	for port := start; port < start+blockSize; port++ {
		if !isFree(port) {
			return false
		}
	}
	return true
}

// portFree decides whether a port is free. Tests replace it to force a busy host.
var portFree = canBind

// canBind reports whether port on 127.0.0.1 is free right now.
func canBind(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	return l.Close() == nil
}

// PortRange returns the block as "first-last", or "" when the worktree has no port.
func PortRange(rec Record) string {
	if rec.Port == 0 {
		return ""
	}
	return fmt.Sprintf("%d-%d", rec.Port, rec.Port+blockSize-1)
}

// PortEnv returns the port variables for a worktree, or nil when it has no port.
func PortEnv(rec Record) map[string]string {
	if rec.Port == 0 {
		return nil
	}
	return map[string]string{
		"DYNA_PORT":  strconv.Itoa(rec.Port),
		"DYNA_PORTS": PortRange(rec),
		"PORT":       strconv.Itoa(rec.Port),
	}
}

// PortEnvForDir returns the port variables a worker in dir should get.
// A directory outside every tracked worktree gets the same names set to empty,
// so a worker never keeps the ports of the worktree that started it.
func PortEnvForDir(ctx context.Context, dir string) map[string]string {
	rec, err := ForDir(ctx, dir)
	if err == nil && rec.Port != 0 {
		return PortEnv(rec)
	}
	return map[string]string{"DYNA_PORT": "", "DYNA_PORTS": "", "PORT": ""}
}

// ForDir returns the record of the registered worktree that contains dir.
// Git reports physical paths, while records keep the configured path, so the
// match resolves symlinks on both sides.
func ForDir(ctx context.Context, dir string) (Record, error) {
	root, err := RepositoryRoot(ctx, dir)
	if err != nil {
		return Record{}, err
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Record{}, fmt.Errorf("resolve worktree root: %w", err)
	}
	records, err := List()
	if err != nil {
		return Record{}, err
	}
	for _, rec := range records {
		if got, err := filepath.EvalSymlinks(rec.Path); err == nil && got == want {
			return rec, nil
		}
	}
	return Record{}, fmt.Errorf("worktree %s is not registered", root)
}
