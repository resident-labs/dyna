package structuredoutput

import (
	"strings"
	"testing"
)

func TestCompiledSchemaValidatesCorrections(t *testing.T) {
	schema, err := Compile(`{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"bad JSON", "{}", `{"answer":"fixed"}`} {
		_, err := schema.Validate(raw)
		if raw == `{"answer":"fixed"}` {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			if err == nil {
				t.Fatalf("accepted %q", raw)
			}
			if correction := SchemaCorrection(err, raw, false); strings.Contains(correction, "Previous response") {
				t.Fatal(correction)
			}
			if correction := SchemaCorrection(err, raw, true); !strings.Contains(correction, "Previous response") {
				t.Fatal(correction)
			}
		}
	}
}
