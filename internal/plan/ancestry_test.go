package plan_test

import (
	"context"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/plan"
	"dyna-agent/internal/websvc"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanAncestryIgnoresLegacyHeader(t *testing.T) {
	testAncestryEngine(t)
	dir := t.TempDir()
	src := "return 1"
	p, err := plan.Ensure(dir, src, plan.Options{WriteAncestry: plan.StampAncestry, ResumedFrom: "wf_parent"})
	if err != nil {
		t.Fatal(err)
	}
	p.ResumedFrom = "wf_wrong"
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := plan.ReadFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResumedFrom != "wf_parent" {
		t.Fatalf("ancestry = %q; want authoritative row wf_parent", got.ResumedFrom)
	}
}

func TestAncestryReportsEngineFailureWithoutLocalFallback(t *testing.T) {
	s := testAncestryEngine(t)
	dir := t.TempDir()
	if _, err := plan.Ensure(dir, "return 1", plan.Options{}); err != nil {
		t.Fatal(err)
	}
	calls, err := websvc.Bind([]websvc.Binding{{
		Name: "StampPlanAncestry", Access: websvc.Write, Scope: websvc.ScopeOperate,
		Method: func(context.Context, string, string) (string, error) {
			return "", errors.New("engine refused ancestry")
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, s.DataDir, calls)
	if _, err := plan.Ensure(dir, "return 1", plan.Options{WriteAncestry: plan.StampAncestry, ResumedFrom: "wf_parent"}); err == nil || !strings.Contains(err.Error(), "engine refused ancestry") {
		t.Fatalf("failed write = %v", err)
	}
	got, err := s.Parent(context.Background(), dir)
	if err != nil || got != "" {
		t.Fatalf("local fallback row = %q,%v", got, err)
	}
}

func TestAncestryMissingWriterReturnsError(t *testing.T) {
	s := testAncestryEngine(t)
	dir := t.TempDir()
	if _, err := plan.Ensure(dir, "return 1", plan.Options{ResumedFrom: "wf_parent"}); !errors.Is(err, plan.ErrAncestry) {
		t.Fatalf("missing writer = %v", err)
	}
	got, err := s.Parent(context.Background(), dir)
	if err != nil || got != "" {
		t.Fatalf("missing writer saved %q,%v", got, err)
	}
}

func TestGraphFailureOnlyStopsRequiredAncestry(t *testing.T) {
	for _, parent := range []string{"", "wf_parent"} {
		t.Run("parent_"+parent, func(t *testing.T) {
			testAncestryEngine(t)
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "plan.json"), 0700); err != nil {
				t.Fatal(err)
			}
			p := plan.Extract("return 1", plan.Options{ResumedFrom: parent})
			err := plan.WriteAtomic(dir, p, plan.StampAncestry)
			if err == nil {
				t.Fatal("graph staging unexpectedly succeeded")
			}
			if got := errors.Is(err, plan.ErrAncestry); got != (parent != "") {
				t.Fatalf("ancestry failure = %v, want %v: %v", got, parent != "", err)
			}
		})
	}
}

func TestEnsureStaleGraphStillPersistsResumeAncestry(t *testing.T) {
	testAncestryEngine(t)
	dir := t.TempDir()
	first, err := plan.Ensure(dir, "return 1", plan.Options{})
	if err != nil {
		t.Fatal(err)
	}
	kept, err := plan.Ensure(dir, "return 2", plan.Options{ResumedFrom: "wf_parent", WriteAncestry: plan.StampAncestry})
	if err != nil {
		t.Fatal(err)
	}
	if kept.PlanHash != first.PlanHash || kept.ResumedFrom != "wf_parent" {
		t.Fatalf("stale graph lost ancestry: %#v", kept)
	}
}
