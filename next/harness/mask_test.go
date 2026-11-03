package harness

import "testing"

func TestMask(t *testing.T) {
	unchanged := []string{
		"key: &EntityCapabilityKey<T>,",
		"let key = (pos.x, pos.y, pos.z);",
		"let token = Token::new();",
		"let password = String::new();",
		"fn get(&self, key: &str) -> Option<&V>",
		"{kind:\"day\",key:`day-${d}`}",
		"const key = items[0].id;",
		"const token: string = opts.token;",
		"key := fmt.Sprintf(\"%s/%s\", a, b)",
		"token = <redacted>",
		"password: $PASSWORD",
		"secret = some_long_identifier_name",
		"key: \"id\"",
	}
	for _, in := range unchanged {
		if got := Mask(in); got != in {
			t.Errorf("Mask(%q) = %q, want unchanged", in, got)
		}
	}
	masks := map[string]string{
		"GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123":             "GITHUB_TOKEN=[masked]",
		"API_KEY=abcd1234efgh5678":                                    "API_KEY=[masked]",
		`"password": "s3cr3t-long-value"`:                             `"password": [masked]`,
		"export AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCY": "export AWS_SECRET_ACCESS_KEY=[masked]",
		"password: 'correct horse'":                                   "password: 'correct horse'",
		"x ghp_abcdefghijklmnopqrstuvwxyz0123 y":                      "x [masked] y",
	}
	for in, want := range masks {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}
