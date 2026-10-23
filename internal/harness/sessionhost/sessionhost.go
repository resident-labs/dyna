// Package sessionhost feeds one long-lived harness session process across
// the turns of one session, across processes and across engine restarts.
//
// Each session key has one detached host process (this binary re-executed
// with a private argument, like the processguard helper). The host owns one
// child through processguard, the only supervisor, and serves it over a Unix
// socket. Turns are socket clients: they attach, speak the harness protocol
// as raw LF-delimited frames, and detach. The codec above owns protocol ids,
// handshakes and session loading; the host owns the process.
//
// There is no registry: the socket file is the registry. A stale socket
// (nothing listening) is replaced under a file lock. One turn is attached at
// a time; a second attach to the same key is refused, even with a different
// fingerprint. A clean idle detach keeps the child. An abandoned attachment
// (DetachAbandoned, or a client that vanished mid-run) kills the child's
// group at once. A host with no child and no client exits; a session left
// quiet for ten minutes closes.
package sessionhost

import (
	"context"

	"dyna-agent/internal/processguard"
)

// DetachReason tells the host why the turn ended its attachment.
type DetachReason int

const (
	// DetachIdle ends a turn whose run settled: the child stays for the next
	// turn and may idle out.
	DetachIdle DetachReason = iota
	// DetachAbandoned ends a turn whose supervision stopped mid-run: the host
	// kills the child's group, so it never keeps working unwatched.
	DetachAbandoned
)

// Launch names one harness session process to get or start. Key is the
// caller's stable identity for the session, including the harness namespace
// and the home storing it, never a native protocol id learned only after
// attach. Argv, Env and Dir are the resolved child command line, environment
// and working directory. Fingerprint identifies the launch (executable,
// environment, arguments minus the session selector); a live process whose
// fingerprint differs is replaced, and the harness session file carries the
// conversation. Env travels only over the 0600 socket, never argv.
type Launch struct {
	Harness     string
	Key         string
	Fingerprint string
	Argv        []string
	Env         []string
	Dir         string
	// BindLease binds the child's processguard reference to the calling
	// turn's resource leases. The host asks for it before it starts a child
	// and on every attach of a live one, so each turn's leases protect the
	// long-lived process. The reference is the same across attaches of one
	// child and changes when the child is replaced.
	BindLease func(processguard.Reference) error
	// OnStart reports the pid of a child this call actually spawned, once.
	// A call that attaches a live child never fires it, so it is also the
	// signal that the child's protocol state is cold.
	OnStart func(pid int)
}

// Session is one attached harness session process.
type Session interface {
	// Write sends one protocol frame to the child's stdin, adding the LF.
	Write(frame []byte) error
	// Next returns the next frame the child wrote to its stdout, blocking
	// until one arrives, the process exits, or ctx ends. A canceled ctx never
	// stops the child. A process exit is reported as an error carrying
	// bounded stderr text.
	Next(ctx context.Context) ([]byte, error)
	// Replace stops the child and starts launch's in its place on the same
	// session: a model change, account switch or updated bundle.
	Replace(ctx context.Context, launch Launch) error
	// Detach ends the turn's attachment and never blocks. The host's release
	// completes in the background (bounded to a few seconds); the next
	// Session call on the same Host waits for it, so it is never refused.
	Detach(reason DetachReason)
	// Close stops the child and the host. The next Session call starts a
	// replacement.
	Close() error
}

// Host returns the session process for launch's key, starting one when none
// matches, and attaches the calling turn. A key occupied by an attached
// turn is an error, never a silent second writer.
type Host interface {
	Session(ctx context.Context, launch Launch) (Session, error)
}

// LocalExtension is what sessions implement beyond Session: the adapters'
// stop paths and per-turn diagnostics. Both the in-process and the socket
// sessions implement it.
type LocalExtension interface {
	// Halt stops the child's group the way a canceled turn does:
	// terminate, grace, kill.
	Halt()
	// KillGroup immediately kills the child's process group.
	KillGroup()
	// StderrTail drains pending pipe bytes, then returns the child's bounded
	// stderr written since this turn's attach. It does not wait for child exit.
	StderrTail() string
}

// defaultHost is the one shared detached host every harness adapter uses.
var defaultHost = newSocketHost("")

// Default returns the shared detached session host. Adapters never build
// their own registry beside it.
func Default() Host { return defaultHost }

// ResetForTest stops the child and host of every session this process
// attached through Default; tests use it to leave nothing behind.
func ResetForTest() { defaultHost.shutdownUsed() }
