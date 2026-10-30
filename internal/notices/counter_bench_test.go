package notices

import (
	"dyna-agent/internal/database"
	"testing"
)

func BenchmarkCounter(b *testing.B) {
	s := Store{Dir: b.TempDir()}
	defer database.Close(s.Dir)
	if _, err := s.Post(testNotice("key")); err != nil {
		b.Fatal(err)
	}
	b.Run("Read", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := s.Revision(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Write", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			n := testNotice("key")
			n.Text = string(rune('a' + i%2))
			if _, err := s.Post(n); err != nil {
				b.Fatal(err)
			}
		}
	})
}
