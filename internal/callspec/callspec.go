// Package callspec holds what the engine and its clients agree on about a
// call, so neither side imports the other. Each call ends within its
// deadline on both sides of the socket: the client stops waiting, and the
// engine cancels the request context.
package callspec

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"
	"time"
)

// CodeEngineStopping is the reply code of a call that the engine canceled while
// it stops or restarts. The reply is a 503: the caller sends the same request
// again to the engine that starts next (D-269).
const CodeEngineStopping = "engine_stopping"

// ErrRetryable marks a write the engine could not apply yet but may apply
// later, so the caller keeps the request and sends it again with the same
// request ID instead of dropping it.
var ErrRetryable = errors.New("temporarily unavailable write")

// Retryable wraps err as a condition the caller may retry.
func Retryable(err error) error { return fmt.Errorf("%w: %w", ErrRetryable, err) }

// callTimeout is the deadline of every call that LongCalls does not name.
const callTimeout = 5 * time.Second

// LongCalls are the calls whose work can take longer than 5 s (gh, git,
// models, starting or installing a harness), each with its own deadline.
// Item 8 moves such work out of the call; until then a call stays here with
// a reason. A test fails when this list grows.
var LongCalls = map[string]time.Duration{
	// Read-only input waits last 30 seconds and leave time for transport.
	"RunnerWaitInput": time.Minute,
	// A run agent wait lasts RunAgentWaitBound and leaves time for transport.
	"WaitRunAgent": time.Minute,
	// Closing a run agent stops its turn, which can take 25 seconds before SIGKILL.
	"CloseRunAgent": time.Minute, "CloseRunAgents": time.Minute,
	// Librarian setup resolves git, installs hooks, and can close a session.
	"EnableLibrarian": 2 * time.Minute, "DisableLibrarian": 2 * time.Minute,
	// A wake syncs a git worktree and starts or assigns a harness turn.
	"WakeLibrarian":          2 * time.Minute,
	"WakeLibrarianSmellsNow": 2 * time.Minute,
	// Smell validation resolves git and waits for the repository smell lock.
	"FileLibrarySmell": 30 * time.Second,
	// Posting delivers to sessions, which can start a harness: delivery to a
	// room's root can close it and start a new root (notify.deliverRoot).
	// Item 8 commits the post and returns at once, with delivery after it.
	"Send": 30 * time.Second, "SendFromCard": 30 * time.Second, "SendToSession": 30 * time.Second, "Post": 30 * time.Second,
	// Giving the root seat back delivers its handoff the same way.
	"ReturnSeat": 30 * time.Second,
	// Taking the root seat gives the internal root up to 30 s to write its
	// handoff, then stops its turn.
	"AttachSeat": time.Minute,
	// Stopping or starting sessions and runs waits for their processes.
	"StopSession": time.Minute, "BulkSessionAction": time.Minute, "CloseSession": time.Minute, "ForceCloseSession": time.Minute, "CloseSessionOptions": time.Minute,
	"ReopenSession": 2 * time.Minute, "PromoteSession": 2 * time.Minute, "TransferSession": 2 * time.Minute,
	"ConsolidateSessions": 2 * time.Minute, "ResumeSessionConsolidation": 2 * time.Minute, "RestartReviewer": 2 * time.Minute,
	"StartRun": 30 * time.Second, "CancelRun": 30 * time.Second,
	"DeleteRoom": 30 * time.Second,
	// Removing runs applies their worktree disposition (git), and archiving
	// across filesystems copies and verifies a whole run folder: 4 s for the
	// largest live run (407 MB) on one disk.
	"RemoveRun": 2 * time.Minute, "ClearRuns": 2 * time.Minute,
	"ArchiveRun": 2 * time.Minute, "UnarchiveRun": 2 * time.Minute,
	// Worktree creation fetches git and may restore dependencies. Cleanup scans
	// repositories and removes worktrees. Keep their existing synchronous CLI
	// behavior while item 8 moves slow work into jobs.
	"NewWorktree": 10 * time.Minute,
	// Starting a session creates its worktree and restores dependencies, the
	// same work as NewWorktree, before it answers. The caller reports the
	// session if the answer still arrives late.
	"StartSubagent": 10 * time.Minute,
	"GCWorktrees":   2 * time.Minute, "RemoveWorktrees": 2 * time.Minute,
	"PruneWorktrees": 2 * time.Minute, "ApplyRunWorktreeDisposition": 2 * time.Minute,
	// A machine probe waits for one SSH connection: the prober allows the
	// machine's connect timeout plus 10 s, and then stores what it read. The
	// connect timeout is at most 10 minutes (machines.MaxSSHConnectTimeout), so
	// 11 minutes covers the longest probe and its store. With the 5 s default
	// the request deadline cancels a slow probe before it can record that the
	// machine is offline.
	"ProbeNode": 11 * time.Minute,
	// Merge recovery can delete trial branches, build a retry, or publish a
	// passed trial through git and GitHub. Keep the existing synchronous CLI
	// behavior until item 8 moves slow work into jobs.
	"MergeControl": 2 * time.Minute,
	// GitHub (gh) and git.
	"ExportIssue": 30 * time.Second, "GitHubList": 2 * time.Minute, "GitHubView": 2 * time.Minute, "GitHubDiff": 2 * time.Minute,
	"CommentPR": 2 * time.Minute, "EditPR": 2 * time.Minute, "PostPRReview": 2 * time.Minute, "LearnPRNoise": 2 * time.Minute, "TrackPR": 2 * time.Minute, "TrackPRRef": 2 * time.Minute, "SetPRDraft": 2 * time.Minute, "OpenChange": 2 * time.Minute, "MergeFixPR": 2 * time.Minute,
	// Verdicts refresh GitHub or git and deliver notices before returning.
	"RecordReviewResult": 2 * time.Minute, "RecordPRVerdict": 2 * time.Minute, "StartIssueWork": 2 * time.Minute, "GitStatus": 2 * time.Minute, "GitLastCommits": 2 * time.Minute,
	"GitReadFile": 2 * time.Minute, "GitHistory": 2 * time.Minute, "GitBlame": 2 * time.Minute, "GitDiff": 2 * time.Minute,
	"GitExport": 2 * time.Minute, "SyncRefresh": 2 * time.Minute,
	// Git fetch of every remote can take up to 2 minutes each.
	// Item 8 will make fetch asynchronous and remove this long call.
	"SyncFetch": 2 * time.Minute,
	// Provider and model requests, hooks and updates.
	"AccountsList": 2 * time.Minute, "AccountsAdd": 2 * time.Minute, "AccountsImport": 2 * time.Minute, "ProfileModels": 2 * time.Minute, "ConfigCheckProfile": 2 * time.Minute,
	"RunHook": 2 * time.Minute, "TestHook": 2 * time.Minute, "UpdateDyna": 2 * time.Minute,
	"VoiceStart": 2 * time.Minute, "VoiceResume": 2 * time.Minute, "VoiceStop": 30 * time.Second,
	// A web search is one or two provider requests. The ChatGPT provider streams
	// an answer the model writes after it searches, which takes longer than 5 s.
	"WebSearch": time.Minute,
	// Stopping a shell waits up to 10 s for it to start, 5 s for SIGTERM
	// before SIGKILL, and 10 s for the exit record: 25 s at most. The stop
	// does not follow the call's context, because a shell that got SIGTERM
	// must still get SIGKILL and its exit record after a caller gives up.
	"StopShell": 30 * time.Second,
	// Listing harness runtimes asks each installed harness for its version:
	// 4 s on a cold engine in live QA.
	"PrivateRuntimes": 2 * time.Minute,
	// Installing, syncing and signing in to harness runtimes.
	"InstallPrivateRuntime": 10 * time.Minute, "UninstallPrivateRuntime": 10 * time.Minute, "RefreshPrivateRuntimes": 10 * time.Minute,
	"SyncPrivateRuntime": 10 * time.Minute, "RemovePrivateRuntime": 10 * time.Minute, "BeginPrivateRuntimeLogin": 10 * time.Minute,
	"FinishPrivateRuntimeLogin": 10 * time.Minute, "RestartPrivateRuntimeLogin": 10 * time.Minute, "FindPersonalLogin": 10 * time.Minute,
	"UsePersonalLogin": 10 * time.Minute, "ImportPrivateConfig": 10 * time.Minute,
	"GCPrivateRuntime": 10 * time.Minute,
	"AccountsUse":      10 * time.Minute, "AccountsRemove": 10 * time.Minute,
	"AddHarnessMCP": 10 * time.Minute, "AddHarnessSkill": 10 * time.Minute,
	// CLI catalog edits generate private harness files synchronously, like the
	// browser add calls above. Item 8 will move generation to a background job.
	"AddCatalogMCP": 10 * time.Minute, "AddCatalogSkill": 10 * time.Minute,
	"RemoveCatalogEntry": 10 * time.Minute, "SetCatalogEntryEnabled": 10 * time.Minute,
	// A say dials a voice provider and waits for the whole clip; it ends
	// itself after voice.SayTimeout (60 s), and the call leaves time for transport.
	"VoiceSay": 75 * time.Second,
	// Explicit account sync performs the same synchronous private generation.
	"SyncCatalogAccount": 10 * time.Minute,
}

// Deadline is how long the engine may take to answer method.
func Deadline(method string) time.Duration {
	if d, ok := LongCalls[method]; ok {
		return d
	}
	return callTimeout
}

// lateError is a call that ran past its deadline.
type lateError struct {
	method string
	d      time.Duration
}

func (e lateError) Error() string {
	return fmt.Sprintf("engine did not answer %s in %g s", e.method, e.d.Seconds())
}

// Late is the error of a call that ran past its deadline d. The engine and
// the client both return it, so an agent sees one line whichever side stops
// first.
func Late(method string, d time.Duration) error { return lateError{method, d} }

// IsLate reports a call that ran past its deadline, so a caller can ask
// whether the engine applied it before giving up.
func IsLate(err error) bool {
	var late lateError
	return errors.As(err, &late)
}

// VersionHeader carries the build of the sender, both ways on every call.
const VersionHeader = "X-Dyna-Version"

// Build is this binary's build, sent in VersionHeader. The dyna command sets
// it at start; it is empty in tests that do not set it.
var Build string

// VersionGap is the one line for a call across two builds that cannot work.
func VersionGap(engine, client string) string {
	return fmt.Sprintf("engine is %s, this dyna is %s: restart the engine or update dyna", engine, client)
}

// IsCutOff reports a call whose connection closed before the engine answered,
// as when the engine restarts for an update. The engine may or may not have
// applied the call.
func IsCutOff(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		// net/http reports a connection that closed before the reply as this
		// unexported error when its reader sees the close before the request is
		// registered. It has no exported value to match.
		err != nil && strings.Contains(err.Error(), "http: server closed idle connection")
}
