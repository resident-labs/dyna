package scope

import (
	"reflect"
	"testing"
)

func TestNormalizeRejectsUnknownAndOrders(t *testing.T) {
	got, err := Normalize([]string{" Terminal ", "read", "read"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{Read, Terminal}) {
		t.Fatalf("scopes = %#v", got)
	}
	if _, err = Normalize(nil); err == nil {
		t.Fatal("empty list accepted")
	}
	if _, err = Normalize([]string{"shell"}); err == nil {
		t.Fatal("unknown scope accepted")
	}
}

func TestLabelSeparatesLegacyFullAccess(t *testing.T) {
	if Label(All(), true) != LegacyFull {
		t.Fatalf("legacy label = %q", Label(All(), true))
	}
	if Label(Default(), false) != "read, operate" {
		t.Fatalf("default label = %q", Label(Default(), false))
	}
	if Label(nil, false) != "no access" {
		t.Fatalf("empty label = %q", Label(nil, false))
	}
}
