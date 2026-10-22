package plan_test

import (
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/plan/ancestry"
	"dyna-agent/internal/store/storetest"
)

func testAncestryEngine(t *testing.T) ancestry.Store {
	t.Helper()
	storetest.DataDir(t)
	t.Setenv("DYNA_ARCHIVE_DIR", "")
	return enginetest.ServePlanAncestry(t)
}
