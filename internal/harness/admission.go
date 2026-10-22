package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
)

// nativeAdmissionError never enters native recovery or account failover. The
// cause keeps an uncertain outbox request's original ID available to callers.
type nativeAdmissionError struct{ cause error }

func (e *nativeAdmissionError) Error() string { return fmt.Sprintf("native admission: %v", e.cause) }
func (e *nativeAdmissionError) Unwrap() error { return e.cause }

func isNativeAdmissionError(err error) bool {
	var admission *nativeAdmissionError
	return errors.As(err, &admission)
}

// Validate the acknowledgement inside the outbox decoder, so a missing
// reference returns UnacknowledgedError with the original ordinary request ID.
type nativeAdmissionReply struct{ ref callspec.ExecutionRef }

func (reply *nativeAdmissionReply) UnmarshalJSON(raw []byte) error {
	var ref callspec.ExecutionRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		return err
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.AdmissionID == "" {
		return errors.New("native admission acknowledgement has no admission ID")
	}
	reply.ref = ref
	return nil
}

func (r *journaledRun) admitNativeAttempt(ctx context.Context) error {
	if !r.p.DynaManaged() || r.exec.ProcessRunner != nil {
		return nil
	}
	if r.exec.Outbox == nil && r.exec.Execution == (callspec.ExecutionRef{}) {
		// Explicit engine-local embedding does not use a detached runner outbox.
		return nil
	}
	if err := ctx.Err(); err != nil {
		return &nativeAdmissionError{err}
	}
	if r.exec.Outbox == nil {
		return &nativeAdmissionError{errors.New("execution needs its runner outbox")}
	}
	parent := r.exec.Execution
	if err := parent.Validate(); err != nil {
		return &nativeAdmissionError{err}
	}
	if parent.AdmissionID != "" {
		return &nativeAdmissionError{errors.New("execution parent already has an admission ID")}
	}
	var reply nativeAdmissionReply
	input := callspec.AccountAdmission{ExecutionRef: parent, Profile: r.p.Name, Harness: r.p.Harness, AccountID: r.accountID, WorkDir: r.cwd}
	requestID, err := r.exec.Outbox.CallResultWithinID(ctx, &reply, "AdmitNativeAttempt", input)
	if err != nil {
		return &nativeAdmissionError{err}
	}
	admitted := reply.ref
	returnedParent := admitted
	returnedParent.AdmissionID = ""
	if returnedParent != parent || admitted.AdmissionID != requestID {
		return &nativeAdmissionError{&engineclient.UnacknowledgedError{RequestID: requestID, Cause: errors.New("engine returned a mismatched execution reference or admission ID")}}
	}
	r.execution = admitted
	return nil
}
