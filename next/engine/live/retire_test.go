package live

import "testing"

func TestRetireDoesNotBlockReplacement(t *testing.T) {
	l := New(nil)
	first, _ := l.CreateSession("session")
	retired := l.RetireSession("session")
	if retired != first {
		t.Fatal("first handle not retired")
	}
	retired.Unlock()
	second, ok := l.CreateSession("session")
	if !ok || second == first {
		t.Fatal("stale publisher cannot create a replacement handle")
	}
	if l.RetireHandle(first) != nil {
		t.Fatal("stale retirement succeeded")
	}
	if got, ok := l.LookupSession("session"); !ok || got != second {
		t.Fatal("stale retirement removed the replacement")
	}
	retired = l.RetireHandle(second)
	if retired != second {
		t.Fatal("replacement was not retired")
	}
	retired.Unlock()
	if _, ok := l.LookupSession("session"); ok {
		t.Fatal("replacement remained after validation")
	}
}
