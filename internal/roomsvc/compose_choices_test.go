package roomsvc

import (
	"reflect"
	"testing"
)

func TestChoicesInternalRootAppearsOnce(t *testing.T) {
	got := Choices([]string{"root", "lead"})
	want := []string{"@team", "@root", "@lead"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Choices = %v, want %v", got, want)
	}
}
