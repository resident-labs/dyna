package workflowsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/childenv"
	"dyna-agent/internal/mode"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/replayreason"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workflow"
)

type ReplayReason = replayreason.WorkflowReplayReason

const (
	ReplayEligible         = replayreason.Eligible
	ReplayLegacy           = replayreason.Legacy
	ReplayNotDeterministic = replayreason.NotDeterministic
	ReplayIncomplete       = replayreason.Incomplete
	ReplayUncertain        = replayreason.Uncertain
	ReplayUnsafe           = replayreason.Unsafe
	ReplayInputMismatch    = replayreason.InputMismatch
	ReplayLaunchUncertain  = replayreason.LaunchUncertain
)

func replayEligibility(inputs runstore.WorkflowReplayInputs, manifest runstore.WorkflowManifest, fingerprint string) ReplayReason {
	if !inputs.Deterministic {
		return ReplayNotDeterministic
	}
	if manifest.UnsafeReason != "" {
		return ReplayUnsafe
	}
	for _, call := range manifest.Calls {
		if call.Result == nil {
			return ReplayUncertain
		}
	}
	if !manifest.Complete {
		return ReplayIncomplete
	}
	if inputs.Fingerprint == "" || fingerprint == "" || inputs.Fingerprint != fingerprint {
		return ReplayInputMismatch
	}
	return ReplayEligible
}

func prepareSafeReplay(ctx context.Context, opts Options, profiles *profile.Store) (*workflow.ReplayBarrier, error) {
	if opts.ResumeFrom == "" {
		return nil, fmt.Errorf("safe replay needs a source run")
	}
	inputs, err := runstore.ReadWorkflowReplayInputs(ctx, opts.ResumeFrom)
	if err != nil {
		return nil, fmt.Errorf("read replay inputs: %w", err)
	}
	manifest, err := runstore.ReadWorkflowManifest(ctx, opts.ResumeFrom)
	if err != nil {
		return nil, fmt.Errorf("read replay manifest: %w", err)
	}
	head, err := replayWorktreeHead(ctx, opts.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("safe replay worktree: %w", err)
	}
	fingerprint, _, err := replayFingerprint(opts, profiles, os.Environ(), head)
	if err != nil {
		return nil, fmt.Errorf("safe replay inputs: %w", err)
	}
	if reason := replayEligibility(inputs, manifest, fingerprint); reason != ReplayEligible {
		return nil, fmt.Errorf("safe replay refused: %s", reason)
	}
	return workflow.NewReplayBarrier(manifest)
}

var autoReplayLaunch = Launch

// TryAutomaticReplay resumes a stopped run at most once. Every refusal keeps
// the ordinary interrupted-run report in place.
func TryAutomaticReplay(ctx context.Context, runID string) (string, ReplayReason, error) {
	meta, err := runstore.ReadMeta(runID)
	if err != nil {
		return "", "", err
	}
	if meta.Status != runstore.StatusInterrupted {
		return "", ReplayUnsafe, nil
	}
	inputs, err := runstore.ReadWorkflowReplayInputs(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ReplayLegacy, nil
	}
	if err != nil {
		return "", "", err
	}
	if !inputs.Deterministic {
		return "", ReplayNotDeterministic, nil
	}
	manifest, err := runstore.ReadWorkflowManifest(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ReplayLegacy, nil
	}
	if err != nil {
		return "", "", err
	}
	// Check the manifest before doing filesystem work.
	if reason := replayEligibility(inputs, manifest, inputs.Fingerprint); reason != ReplayEligible {
		return "", reason, nil
	}
	var opts Options
	if err := json.Unmarshal(inputs.Options, &opts); err != nil {
		return "", ReplayIncomplete, nil
	}
	profiles, err := profile.Load(profile.DefaultPath())
	if err != nil {
		return "", "", err
	}
	actor := authority.Actor{Role: authority.Owner}
	if meta.Session != "" {
		actor = authority.Actor{Role: authority.Session, Session: meta.Session}
	}
	roomEnv := RoomRunEnvironment(actor, room.Record{ID: meta.RoomID, Name: meta.Room})
	candidates := [][]string{childenv.WithoutLocks(os.Environ())}
	if meta.RoomID != "" || meta.Room != "" {
		candidates = append(candidates, roomEnv)
		if meta.ParentSession != "" {
			candidates = append(candidates, append(slices.Clone(roomEnv), mode.EnvRunResultTo+"="+meta.ParentSession))
		}
	}
	head, err := replayWorktreeHead(ctx, opts.WorkDir)
	if err != nil {
		return "", ReplayInputMismatch, nil
	}
	var env []string
	for _, candidate := range candidates {
		candidate = slices.DeleteFunc(candidate, func(v string) bool { return strings.HasPrefix(v, mode.EnvRunDetached+"=") })
		candidate = append(candidate, mode.EnvRunDetached+"=1")
		fingerprint, _, err := replayFingerprint(opts, profiles, candidate, head)
		if err != nil {
			return "", ReplayInputMismatch, nil
		}
		if replayEligibility(inputs, manifest, fingerprint) == ReplayEligible {
			env = candidate
			break
		}
	}
	if env == nil {
		return "", ReplayInputMismatch, nil
	}
	claimed, err := runstore.ClaimWorkflowReplay(ctx, runID)
	if err != nil {
		return "", "", err
	}
	if !claimed {
		return "", ReplayLaunchUncertain, nil
	}
	opts.ResumeFrom, opts.SafeReplay = runID, true
	id, err := autoReplayLaunch(ctx, opts, LaunchOptions{Env: env})
	if err != nil {
		return "", ReplayLaunchUncertain, nil
	}
	return id, ReplayEligible, nil
}
