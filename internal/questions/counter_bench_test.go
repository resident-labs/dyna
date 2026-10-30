package questions

import (
	"dyna-agent/internal/database"
	"path/filepath"
	"testing"
)

func BenchmarkCounter(b *testing.B) {
	dir := b.TempDir()
	s := &Store{Root: filepath.Join(dir, "questions")}
	defer database.Close(dir)
	if _, err := s.Revision(); err != nil {
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
			if err := s.update(func(st *State) error { st.Next["room"]++; return nil }); err != nil {
				b.Fatal(err)
			}
		}
	})
}
