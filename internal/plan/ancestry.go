package plan

import (
	"context"
	"errors"
	"fmt"
)

// AncestryWriter persists a parent through the caller's engine boundary.
type AncestryWriter func(context.Context, string, string) (string, error)

// ErrAncestry marks a failure to preserve lineage, which must stop a workflow.
var ErrAncestry = errors.New("workflow ancestry")

func stampAncestry(ctx context.Context, write AncestryWriter, dir, parent string) (string, error) {
	if write == nil {
		return "", fmt.Errorf("%w: no engine writer supplied", ErrAncestry)
	}
	result, err := write(ctx, dir, parent)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrAncestry, err)
	}
	return result, nil
}
