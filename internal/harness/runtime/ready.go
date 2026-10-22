package runtime

import (
	"context"
	"errors"
)

// HarnessReady reports whether Dyna has its private copy of harness at
// version ("" for the default), or can install one on first use now. Like
// first-use install, an exact version needs no latest-version metadata. The
// error names the fix for the owner.
func HarnessReady(ctx context.Context, harness, version string) (installed bool, err error) {
	a, err := Adapter(harness)
	if err != nil {
		return false, err
	}
	if _, err := NewBundleManager().Resolve(a.Harness, version); err == nil {
		return true, nil
	}
	if version != "" {
		return false, nil
	}
	if _, err := recommendedInstallVersion(ctx, a.Harness); err != nil {
		return false, errors.New("Install " + a.Harness + " on the Harnesses page first.")
	}
	return false, nil
}
