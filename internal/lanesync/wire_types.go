package lanesync

import "time"

type Progress struct {
	Kind      string    `json:"kind"`
	State     string    `json:"state"`
	StartedAt time.Time `json:"startedAt"`
	Head      string    `json:"head"`
	PRURL     string    `json:"prURL"`
	Error     string    `json:"error"`
	Attempts  int       `json:"attempts"`
}

type Commit struct {
	SHA    string `json:"sha"`
	Title  string `json:"title"`
	Author string `json:"author"`
	At     string `json:"at"`
}

type Integration struct {
	Head       string   `json:"head"`
	LastExport string   `json:"lastExport"`
	Commits    []Commit `json:"commits"`
}

type Repository struct {
	Remotes     map[string]Remote `json:"remotes" tstype:"RemoteMapOrNull"`
	Integration Integration       `json:"integration"`
	Operation   *Progress         `json:"operation,omitempty"`
}

type Status map[string]Repository

type Remote struct {
	Ahead     int       `json:"ahead"`
	Behind    int       `json:"behind"`
	Head      string    `json:"remoteHead"`
	Branch    string    `json:"branch"`
	CheckedAt time.Time `json:"checkedAt"`
	LastError string    `json:"lastError,omitempty"`
	Rewrite   string    `json:"rewrite,omitempty"`
}
