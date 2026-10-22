package jsonout

import (
	"bytes"
	"errors"
	"testing"
)

func TestWritePrintsIndentedJSONWithNewline(t *testing.T) {
	var out bytes.Buffer
	if err := Write(&out, map[string]any{"b": 1, "a": []int{2}}); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": [\n    2\n  ],\n  \"b\": 1\n}\n"
	if out.String() != want {
		t.Fatalf("wrote %q, want %q", out.String(), want)
	}
}

type failingWriter struct{}

func (failingWriter) Write(p []byte) (int, error) { return 0, errors.New("write failed") }

func TestWriteReportsWriterErrors(t *testing.T) {
	if err := Write(failingWriter{}, map[string]string{"a": "b"}); err == nil {
		t.Fatal("wanted the writer error")
	}
}
