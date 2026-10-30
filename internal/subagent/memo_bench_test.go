package subagent

import (
	"context"
	"dyna-agent/internal/database"
	"dyna-agent/internal/memo"
	"dyna-agent/internal/sessionlife"
	"path/filepath"
	"testing"
)

func BenchmarkWarmMeta(b *testing.B) {
	root := filepath.Join(b.TempDir(), "sessions")
	if err := sessionlife.Save(root, "test", Meta{Name: "test"}); err != nil {
		b.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(root))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { database.Close(filepath.Dir(root)) })
	c := &metaCache{memo.New[[2]string, sessionlife.Version, Meta](metaCacheSize)}
	for i := 0; i < b.N; i++ {
		if _, err := c.load(context.Background(), db, root, "test"); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N-c.cache.Loads())/float64(b.N), "hits/read")
}
