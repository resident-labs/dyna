package memo

import "testing"

func TestRevisionAndEviction(t *testing.T) {
	m := New[string, int, int](1)
	loads := 0
	load := func() (int, error) { loads++; return loads, nil }
	if v, _ := m.Get("a", 1, load); v != 1 {
		t.Fatal(v)
	}
	if v, _ := m.Get("a", 1, load); v != 1 {
		t.Fatal(v)
	}
	if v, _ := m.Get("a", 2, load); v != 2 {
		t.Fatal(v)
	}
	m.Put("b", 1, 3)
	if _, ok := m.Lookup("a", 2); ok {
		t.Fatal("entry was not evicted")
	}
	if m.Loads() != 3 {
		t.Fatal(m.Loads())
	}
}
func TestLoadOutsideLock(t *testing.T) {
	m := New[string, int, int](2)
	v, err := m.Get("a", 1, func() (int, error) { m.Put("b", 1, 2); return 1, nil })
	if err != nil || v != 1 {
		t.Fatalf("value %d: %v", v, err)
	}
}
func BenchmarkGetHit(b *testing.B) {
	m := New[int, int, int](1)
	m.Put(1, 1, 1)
	load := func() (int, error) { return 1, nil }
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = m.Get(1, 1, load)
	}
}
