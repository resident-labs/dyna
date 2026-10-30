package library

// Entry is one feature in the map.
type Entry struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Parent     string   `json:"parent,omitempty"`
	Summary    string   `json:"summary"`
	Differs    string   `json:"differs,omitempty"`
	Owns       []string `json:"owns"`
	Docs       []string `json:"docs,omitempty"`
	Tests      []string `json:"tests,omitempty"`
	Relates    []string `json:"relates,omitempty"`
	VerifiedAt string   `json:"verifiedAt,omitempty"`
	Retired    bool     `json:"retired,omitempty"`
	Successor  string   `json:"successor,omitempty"`
}

// Finding is one lint warning. Lint never blocks in the first version.
type Finding struct {
	Rule    int    `json:"rule"`
	Entry   string `json:"entry,omitempty"`
	File    string `json:"file,omitempty"`
	Message string `json:"message"`
}
