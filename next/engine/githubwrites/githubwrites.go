// Package githubwrites decides which GitHub writes a room's boxes may make (spec 7.9, "GitHub writes"). It is an allow-list: a
// request passes only if it matches a route or GraphQL mutation of the tables below for a group the room has on, on a repository
// the room allows. Everything else is refused with a typed Refusal.
package githubwrites

import (
	"context"
	"fmt"

	"github.com/resident-labs/dyna/contract"
)

type allowedKey struct{}

// WithAllowed puts a room's grant on a request's context. The API layer calls it with the room of the box socket's caller, so
// nothing a box sends reaches the grant.
func WithAllowed(ctx context.Context, grant contract.GitHubWrites) context.Context {
	return context.WithValue(ctx, allowedKey{}, grant)
}

// AllowedFrom is the grant a context carries: the zero grant, which allows no write, when it has none.
func AllowedFrom(ctx context.Context) contract.GitHubWrites {
	grant, _ := ctx.Value(allowedKey{}).(contract.GitHubWrites)
	return grant
}

// Refusal is a request the githubWrites setting refused. Reason is the typed cause; Detail says what was refused and why.
type Refusal struct {
	Reason contract.GitHubWriteReason
	Detail string
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("%s (room setting githubWrites: %s)", r.Detail, r.Reason)
}

func refuse(reason contract.GitHubWriteReason, format string, args ...any) *Refusal {
	return &Refusal{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}
