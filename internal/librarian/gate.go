package librarian

import (
	"context"
	"strings"

	"dyna-agent/internal/library"
	"dyna-agent/internal/repoid"
)

// Gate decides without a model whether the changes from base to target need
// the librarian: an unmapped source file, an owned file whose change is more
// than whitespace, or an entry whose pin is stale at target. It returns the
// reason, or "" to skip.
func Gate(ctx context.Context, dir, base, target string, entries []library.Entry) (string, error) {
	if base == "" {
		return "first run: build the map", nil
	}
	if len(entries) == 0 {
		return "the map is empty", nil
	}
	changes, err := library.Changed(ctx, dir, base, target, entries)
	if err != nil {
		return "", err
	}
	for _, ch := range changes {
		if len(ch.Owners) == 0 && library.Source(ch.File) {
			return "unmapped source file " + ch.File, nil
		}
	}
	for _, ch := range changes {
		if len(ch.Owners) == 0 {
			continue
		}
		if real, err := meaningful(ctx, dir, base, target, ch.File); err != nil || real {
			return "owned file " + ch.File + " changed (" + strings.Join(ch.Owners, ", ") + ")", err
		}
	}
	// A run that cleared only its own entry leaves the others stale; they
	// keep the librarian running until each is re-verified. Whitespace-only
	// changes since a pin do not count, as above.
	stale, err := library.StaleAt(ctx, dir, target, entries)
	if err != nil {
		return "", err
	}
	var ids []string
	for _, e := range entries {
		if !stale[e.ID] {
			continue
		}
		real := e.VerifiedAt == ""
		if !real {
			changes, err := library.Changed(ctx, dir, e.VerifiedAt, target, []library.Entry{e})
			if err != nil {
				return "", err
			}
			for _, ch := range changes {
				if len(ch.Owners) > 0 {
					if real, err = meaningful(ctx, dir, e.VerifiedAt, target, ch.File); err != nil || real {
						break
					}
				}
			}
			if err != nil {
				return "", err
			}
		}
		if real {
			ids = append(ids, e.ID)
		}
	}
	if len(ids) > 0 {
		return "stale pins on " + strings.Join(ids, ", "), nil
	}
	return "", nil
}

// meaningful reports whether a file changed beyond whitespace between two
// commits. --name-only ignores -w in older Git, so it reads the patch.
func meaningful(ctx context.Context, dir, from, to, file string) (bool, error) {
	out, err := repoid.GitRaw(ctx, dir, "--literal-pathspecs", "diff", "-w", "--ignore-blank-lines", from, to, "--", file)
	return strings.Contains(out, "\n@@ "), err
}
