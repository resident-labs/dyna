package goals

import (
	"dyna-agent/internal/database"
	"path/filepath"
	"testing"
)

func BenchmarkCounter(b *testing.B) {
	dir := b.TempDir()
	s := &Store{Root: filepath.Join(dir, "goals")}
	defer database.Close(dir)
	if _, err := s.Add("room", "owner", Input{Text: "work"}); err != nil {
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
			if err := s.Update(func(st *State) error { st.Rooms["room"].NextID++; return nil }); err != nil {
				b.Fatal(err)
			}
		}
	})
}
