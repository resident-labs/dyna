package management

import (
	"os"
	"testing"
)

func TestDynaCallbacksStayInMemoryAndBindState(t *testing.T) {
	DynaMemoryOAuthCallbacks = true
	t.Cleanup(func() { DynaMemoryOAuthCallbacks = false })
	dir := t.TempDir()
	RegisterOAuthSession("invented-state-a", "anthropic")
	RegisterOAuthSession("invented-state-b", "anthropic")
	dynaPrepareCallback("invented-state-a")
	dynaPrepareCallback("invented-state-b")
	defer dynaClearCallback("invented-state-a")
	defer dynaClearCallback("invented-state-b")
	if _, err := WriteOAuthCallbackFileForPendingSession(dir, "anthropic", "invented-state-b", "invented-code", ""); err != nil {
		t.Fatal(err)
	}
	result, err := dynaWaitCallback("invented-state-b")
	if err != nil || result["state"] != "invented-state-b" || result["code"] != "invented-code" {
		t.Fatal("callback lost state")
	}
	if _, err := WriteOAuthCallbackFileForPendingSession(dir, "anthropic", "invented-state-b", "other-code", ""); err == nil {
		t.Fatal("accepted callback twice")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("callback reached disk")
	}
}
