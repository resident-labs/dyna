package runstore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store/storetest"
)

func TestRunOutputCheckpointDoesNotRecaptureGrowth(t *testing.T) {
	for _, shape := range []string{"unchanged", "empty"} {
		t.Run(shape, func(t *testing.T) {
			dir := storetest.DataDir(t)
			t.Setenv(ArchiveDirEnv, "")
			source := []byte("{\"v\":1,\"kind\":\"message\",\"text\":\"whole\"}\n")
			if shape == "empty" {
				source = nil
			}
			path := filepath.Join(dir, "runs", "wf_a", "agents", "1", "output.jsonl")
			bad := filepath.Join(dir, "runs", "wf_b", "agents", "1", "output.jsonl")
			for _, p := range []string{path, bad} {
				if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, source, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(bad, 0700); err != nil {
				t.Fatal(err)
			}
			if err := Migrate(dir); err == nil {
				t.Fatal("unsafe second artifact was accepted")
			}
			db, err := database.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			check := func() {
				t.Helper()
				var size, end int64
				if err := db.QueryRow("SELECT captured_size,prefix_end FROM output_projection_artifacts WHERE artifact_key='runs/wf_a/agents/1'").Scan(&size, &end); err != nil || size != int64(len(source)) || end != size {
					t.Fatalf("checkpoint = %d/%d: %v", end, size, err)
				}
			}
			check()
			var count int
			if err := db.QueryRow("SELECT count(*) FROM output_projection WHERE artifact_key='runs/wf_a/agents/1'").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("projected rows = %d", count)
			}
			grown := append(bytes.Clone(source), []byte("{\"v\":1,\"kind\":\"message\",\"text\":\"later\"}\n")...)
			if err := os.WriteFile(path, grown, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(bad); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(bad, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := Migrate(dir); err != nil {
				t.Fatal(err)
			}
			check()
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, grown) {
				t.Fatalf("source changed: %v", err)
			}
		})
	}
}
