package configsvc

// Item is one settings key with its effective value and where it came from.
// Kind, Options and Help describe how a client edits it.
type Item struct {
	Key           string `json:"key"`
	Value         any    `json:"value"`
	Source        string `json:"source"`
	LiveVsRestart string `json:"liveVsRestart"`
	// Kind is bool, enum, select, int, bytes or duration.
	Display      string   `json:"display,omitempty"`
	Kind         string   `json:"kind"`
	Options      []string `json:"options,omitempty"`
	OptionLabels []string `json:"optionLabels,omitempty"`
	// OptionNotes is a short line for each option, in Options order, shown
	// under the control when that option is chosen. Empty means no note.
	OptionNotes []string `json:"optionNotes,omitempty"`
	Help        string   `json:"help"`
}

// UpdateNotice is a quiet line about a newer dyna release. It only checks;
// installing stays an explicit `dyna update`, so a window never replaces
// the binary its sessions run under.
type UpdateNotice struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	Text    string `json:"text"`
}
