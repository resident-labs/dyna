package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type completedPreparation struct {
	runningBoxes
	signal      func()
	result      error
	preparation context.Context
	createCtx   context.Context
	creates     int
}

func (a *completedPreparation) Get(ctx context.Context, _ contract.SessionID, _ contract.HostName, _ bool) ([]contract.BoxRepo, error) {
	a.preparation = ctx
	a.signal()
	return nil, a.result
}
func (a *completedPreparation) Create(ctx context.Context, _ BoxSpec) (string, error) {
	a.creates++
	a.createCtx = ctx
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if a.preparation.Err() == nil {
		return "", errors.New("preparation gate was not released before Create")
	}
	if _, bounded := ctx.Deadline(); bounded {
		return "", errors.New("Create inherited the preparation deadline")
	}
	return "completed-image", nil
}

func TestPreparationCompletionPreservesNilAndRealReturnedErrors(t *testing.T) {
	returnedFailure := errors.New("repository preparation failed")
	for _, tc := range []struct {
		name         string
		childSignal  error
		parentSignal bool
		result       error
		wantError    error
		wantState    contract.BoxState
		wantCreates  int
		wantFailures int
	}{
		{name: "completed before release", wantState: contract.BoxStarting, wantCreates: 1},
		{name: "completed with child cancellation", childSignal: context.Canceled, wantState: contract.BoxStarting, wantCreates: 1},
		{name: "completed with child deadline", childSignal: context.DeadlineExceeded, wantState: contract.BoxStarting, wantCreates: 1},
		{name: "returned preparation error", result: returnedFailure, wantError: returnedFailure, wantState: contract.BoxPending, wantFailures: 1},
		{name: "returned deadline", childSignal: context.DeadlineExceeded, result: context.DeadlineExceeded, wantError: context.DeadlineExceeded, wantState: contract.BoxFailed, wantFailures: 3},
		{name: "returned cancellation", childSignal: context.Canceled, result: context.Canceled, wantError: context.Canceled, wantState: contract.BoxPending, wantFailures: 1},
		{name: "parent canceled after completed preparation", parentSignal: true, wantError: context.Canceled, wantState: contract.BoxCreating, wantCreates: 1},
		{name: "parent canceled with preparation error", parentSignal: true, result: returnedFailure, wantError: context.Canceled, wantState: contract.BoxCreating},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newHostRig(t)
			parent, cancelParent := context.WithCancel(rig.ctx)
			defer cancelParent()
			adapter := &completedPreparation{result: tc.result}
			rig.r.boxes, rig.r.repos = adapter, adapter
			rig.r.fresh["b"] = true
			rig.r.createContext = func(ctx context.Context) (context.Context, context.CancelFunc) {
				child, cancel := context.WithCancelCause(ctx)
				adapter.signal = func() {
					if tc.parentSignal {
						cancelParent()
					} else if tc.childSignal != nil {
						cancel(tc.childSignal)
					}
				}
				return child, func() { cancel(context.Canceled) }
			}
			if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec("UPDATE boxes SET state='creating',want='run',image='',failures=0 WHERE id='b'")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			rows, err := rig.st.LifecycleBoxes(rig.ctx)
			if err != nil {
				t.Fatal(err)
			}
			got := rig.r.step(parent, rows[0])
			if tc.wantError == nil && got != nil || tc.wantError != nil && !errors.Is(got, tc.wantError) {
				t.Fatalf("actual preparation result changed: got=%v want=%v", got, tc.wantError)
			}
			after, err := rig.st.LifecycleBoxes(rig.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if adapter.creates != tc.wantCreates || after[0].State != tc.wantState || after[0].Failures != tc.wantFailures {
				t.Fatalf("actual step outcome changed: creates=%d state=%s failures=%d; want creates=%d state=%s failures=%d", adapter.creates, after[0].State, after[0].Failures, tc.wantCreates, tc.wantState, tc.wantFailures)
			}
			if tc.parentSignal && tc.wantCreates != 0 && !errors.Is(adapter.createCtx.Err(), context.Canceled) {
				t.Fatalf("Create lost the parent's cancellation: %v", adapter.createCtx.Err())
			}
			if tc.wantState == contract.BoxStarting && after[0].Image != "completed-image" {
				t.Fatalf("confirmed preparation did not use the separately bounded Create: image=%q", after[0].Image)
			}
		})
	}
}
