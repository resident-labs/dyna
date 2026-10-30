package callrequests

import (
	"dyna-agent/internal/database"
	"testing"
)

func BenchmarkCounter(b *testing.B) {
	s := &Store{Root: b.TempDir()}
	defer database.Close(s.Root)
	if _, _, err := s.Ask("room", "a", "session"); err != nil {
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
			if _, _, err := s.Ask("room", string(rune('a'+i%2)), "session"); err != nil {
				b.Fatal(err)
			}
		}
	})
}
