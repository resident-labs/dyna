package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/account/attempts"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/state"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

// NativeAdmissionBinding exposes the production admission handler to callers
// that serve session runners with their configured rooms and harness runtime.
func NativeAdmissionBinding(rooms *roomsvc.Service, rt *harnessruntime.Runtime) websvc.Binding {
	return (services{rooms: rooms, runtime: rt}).admissionBinding()
}

func (s services) admissionBinding() websvc.Binding {
	return websvc.Binding{Name: "AdmitNativeAttempt", Access: websvc.Write, Scope: websvc.ScopeOperate, Local: true, Method: s.admitNativeAttempt}
}

// checkAdmissionCaller runs before a saved reply can bypass the binding.
// Report-only caller fallback and environment claims never authorize admission.
func checkAdmissionCaller(ctx context.Context, in callspec.AccountAdmission) error {
	if in.AdmissionID != "" {
		return errors.New("the engine supplies the admission ID")
	}
	return checkExecutionCaller(ctx, in.ExecutionRef)
}

// checkExecutionCaller also permits historical execution references. Live
// admission and observations apply their distinct state checks afterward.
func checkExecutionCaller(ctx context.Context, ref callspec.ExecutionRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	id := callspec.RequestOf(ctx)
	if id == "" {
		return errors.New("an execution call needs a request ID")
	}
	caller, local := websvc.CallerFromContext(ctx)
	if !local {
		return errors.New("an execution call needs a verified local caller")
	}
	var prefix string
	switch {
	case caller.Kind == websvc.Session && caller.Session != "" && ref.Session == caller.Session:
		prefix = caller.Session
	case caller.Kind == websvc.Worker && caller.Run != "" && caller.Agent == 0 && ref.Run == caller.Run:
		prefix = caller.Run
	default:
		return errors.New("an execution call needs the session's own runner or the run's verified coordinator")
	}
	return callspec.CheckRunnerRequestID(prefix, id)
}

// admissionOwner contains only the canonical inputs admission uses. A final
// SQL read checks these again without file, profile, Git or credential I/O.
type admissionOwner struct {
	profile string
	dir     string
	room    string
	role    string
	started time.Time
}

func readAdmissionOwner(ctx context.Context, q state.Reader, ref callspec.ExecutionRef) (admissionOwner, error) {
	if ref.Session != "" {
		meta, err := subagent.AdmissionTurn(ctx, q, ref.Session, ref.Turn)
		if err != nil {
			return admissionOwner{}, err
		}
		return admissionOwner{profile: meta.Profile, dir: meta.Dir, room: meta.RoomID, role: meta.Role}, nil
	}
	run, err := runstore.LoadStored(ctx, q, ref.Run)
	if err != nil {
		return admissionOwner{}, err
	}
	if run.ID != ref.Run || run.Status != "running" || !run.EndedAt.IsZero() {
		return admissionOwner{}, errors.New("native admission needs a running workflow run")
	}
	return admissionOwner{room: run.RoomID, started: run.StartedAt}, nil
}

// admitNativeAttempt commits immutable facts and its exact acknowledgement in
// one write. It owns no concurrency slot and never starts or selects an account.
func (s services) admitNativeAttempt(ctx context.Context, in callspec.AccountAdmission) (callspec.ExecutionRef, error) {
	if err := checkAdmissionCaller(ctx, in); err != nil {
		return callspec.ExecutionRef{}, err
	}
	m := s.rooms.Notify.Sessions
	db, err := database.Open(filepath.Dir(m.Store.Root))
	if err != nil {
		return callspec.ExecutionRef{}, err
	}
	w := state.For(db)
	var owner admissionOwner
	if err := w.Read(ctx, func(q state.Reader) error {
		var err error
		owner, err = readAdmissionOwner(ctx, q, in.ExecutionRef)
		return err
	}); err != nil {
		return callspec.ExecutionRef{}, err
	}
	profiles, err := m.ProfileStore()
	if err != nil {
		return callspec.ExecutionRef{}, err
	}
	var registered *profile.Profile
	if in.Session != "" {
		if in.Profile != owner.profile {
			return callspec.ExecutionRef{}, errors.New("native admission profile differs from the session's saved profile")
		}
		var ok bool
		registered, ok = profiles.Get(owner.profile)
		if !ok || registered.Disabled {
			return callspec.ExecutionRef{}, errors.New("native admission needs the session's enabled registered profile")
		}
	} else {
		registered, err = profiles.WorkflowProfile(in.Profile)
		if err != nil {
			return callspec.ExecutionRef{}, err
		}
	}
	if !registered.DynaManaged() {
		return callspec.ExecutionRef{}, errors.New("native admission needs a Dyna-managed harness")
	}
	harness, err := harnessruntime.CanonicalHarness(registered.Harness)
	if err != nil {
		return callspec.ExecutionRef{}, err
	}
	candidateHarness, err := harnessruntime.CanonicalHarness(in.Harness)
	if err != nil || candidateHarness != harness {
		return callspec.ExecutionRef{}, errors.New("native admission harness differs from the registered profile")
	}
	dir, err := admissionDirectory(in.WorkDir)
	if err != nil {
		return callspec.ExecutionRef{}, err
	}
	if in.Session != "" {
		expected, _ := m.EffectiveDir(subagent.Meta{Dir: owner.dir, RoomID: owner.room})
		expected, err = admissionDirectory(expected)
		if err != nil {
			return callspec.ExecutionRef{}, err
		}
		if dir != expected {
			return callspec.ExecutionRef{}, errors.New("native admission directory differs from the session's effective directory")
		}
	}
	// Workflows may use any actual cwd, including one without an origin.
	// Existing account scope rules decide which account may run there.
	repo := harnessruntime.RepoIdentity(ctx, dir)
	account, err := s.runtime.Accounts.CheckPreparedAccount(harness, in.AccountID, repo)
	if err != nil {
		return callspec.ExecutionRef{}, err
	}
	ref := in.ExecutionRef
	ref.AdmissionID = callspec.RequestOf(ctx)
	fact := attempts.Fact{ExecutionRef: ref, AccountID: account.ID, Identity: account.Identity,
		Profile: registered.Name, Harness: harness, Repo: repo, WorkDir: dir, AdmittedAt: time.Now()}
	if err := s.writeNativeAdmission(ctx, w, owner, account, fact); err != nil {
		return callspec.ExecutionRef{}, err
	}
	return ref, nil
}

func (s services) writeNativeAdmission(ctx context.Context, w *state.Writer, owner admissionOwner, account harnessruntime.NativeAccount, fact attempts.Fact) error {
	return w.Write(ctx, hub.Event{}, func(conn *sql.Conn) error {
		current, err := readAdmissionOwner(ctx, conn, fact.ExecutionRef)
		if err != nil {
			return err
		}
		if current != owner {
			return errors.New("the execution changed during admission; prepare the attempt again")
		}
		if err := s.runtime.Accounts.RecheckPreparedAccount(ctx, conn, account, fact.Repo); err != nil {
			return err
		}
		if _, err := attempts.SaveTx(ctx, conn, fact); err != nil {
			return err
		}
		raw, err := json.Marshal(fact.ExecutionRef)
		if err != nil {
			return err
		}
		return w.FinishRequestIn(ctx, conn, "AdmitNativeAttempt", raw)
	})
}

func admissionDirectory(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", errors.New("native admission needs an absolute working directory")
	}
	actual, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("native admission directory: %w", err)
	}
	info, err := os.Stat(actual)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("native admission working directory is not a directory")
	}
	return actual, nil
}
