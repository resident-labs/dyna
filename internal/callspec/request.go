package callspec

import (
	"context"
	"fmt"
	"strings"
)

// RequestHeader carries a call's request ID (requirement 9). A detached
// process gives each write its own ID and sends the same ID when it retries,
// so the engine applies the write once.
const RequestHeader = "X-Dyna-Request"

type requestKey struct{}

// WithRequest returns ctx for a call with request ID id. engineclient sends
// it as RequestHeader, and the engine's web server puts it back on the
// call's context.
func WithRequest(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestKey{}, id)
}

// RequestOf is ctx's request ID, or "".
func RequestOf(ctx context.Context) string {
	id, _ := ctx.Value(requestKey{}).(string)
	return id
}

// CheckRunnerRequestID checks the session prefix shared by live and spooled
// runner calls. Embedded calls without a request ID do not need a prefix.
func CheckRunnerRequestID(session, id string) error {
	if id != "" && !strings.HasPrefix(id, session+"/") {
		return fmt.Errorf("runner request ID %s does not belong to session %s", id, session)
	}
	return nil
}
