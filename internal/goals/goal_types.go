package goals

import "time"

type Edit struct {
	At      time.Time `json:"at"`
	By      string    `json:"by"`
	OldText string    `json:"oldText"`
}

type Outcome struct {
	At      time.Time `json:"at"`
	By      string    `json:"by"`
	Summary string    `json:"summary"`
}

// Amendment is one later correction to a goal's brief. Amendments are
// append-only, so the stored brief stays the brief as it was set.
type Amendment struct {
	At   time.Time `json:"at"`
	By   string    `json:"by"`
	Text string    `json:"text"`
}

type Goal struct {
	ID   string `json:"id"`
	Room string `json:"room"`
	Text string `json:"text"`
	// Detail is the brief stored with a spawn goal: context, owned files,
	// checks and limits. Dyna resends it with Text after compaction.
	Detail string `json:"detail,omitempty"`
	// Amendments correct the brief after it was set; oldest first.
	Amendments []Amendment `json:"amendments,omitempty"`
	Setter     string      `json:"setter"`
	Holders    []string    `json:"holders" tstype:"string[] | null"`
	Shared     bool        `json:"shared"`
	// NextItem allocates the goal's item IDs: i1, i2, ...
	NextItem int `json:"nextItem,omitempty"`
	// ItemsClosed counts the times the goal went from some open items to none.
	// Each of those closes posts one notice keyed by this number, so a reopen
	// and a second close notify the holders again.
	ItemsClosed int       `json:"itemsClosed,omitempty"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Edits       []Edit    `json:"edits" tstype:"Edit[] | null"`
	Done        *Outcome  `json:"done,omitempty"`
	// ExpediteUntil marks the room's one expedited goal until that time.
	// Readers drop an expired value; see Store.Expedite.
	ExpediteUntil *time.Time `json:"expediteUntil,omitempty"`
	// LegacyOwnerHeld marks a goal that an old build left with the owner. Root
	// must ask the owner about it, then drop or finish the goal, or hand it to
	// an open session. Nothing writes it now.
	// TODO: legacy/shim remove later — only goals saved by an old build carry it; remove once every room's owner-held goals are resolved.
	LegacyOwnerHeld bool `json:"legacyOwnerHeld,omitempty"`
}

// Status is one goal's mechanical label with a short reason and its item
// counts. Nothing stores it: every caller computes it from facts.
type Status struct {
	Label   string `json:"label"`
	Reason  string `json:"reason"`
	Done    int    `json:"done"`
	Open    int    `json:"open"`
	Dropped int    `json:"dropped"`
}

// JournalLine is a saved status for one agent in a journal entry.
type JournalLine struct {
	Agent   string   `json:"agent"`
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Goals   []string `json:"goals" tstype:"string[] | null"`
	// Done lists goals of this agent recorded as finished but
	// still open. Their holder is reminded once to close them.
	Done []string `json:"done,omitempty"`
	// Needs is a short, stable key for the action root or the owner must
	// take, such as "owner-pi-login". It stays the same while the action does.
	Needs string `json:"needs,omitempty"`
}

// An Event is one journal line about a goal or item: a note, or an item
// state change. Its ID is "e" and the byte offset of its line. Event lines
// carry kind "event"; a legacy keeper entry, converted by convertJournals in
// sqlite.go, carries no kind.
type Event struct {
	ID    string    `json:"id"`
	Goal  string    `json:"goal"`
	Item  string    `json:"item,omitempty"`
	By    string    `json:"by"`
	Kind  string    `json:"kind"` // always "event"
	Event string    `json:"event"`
	Text  string    `json:"text,omitempty"`
	At    time.Time `json:"at"`
}

// JournalEntry is a saved report. Its ID is "j" and the byte
// offset of its line in the room's journal file, so a cursor finds its place
// without reading the whole file.
type JournalEntry struct {
	ID string `json:"id"`
	// Round is the check-in round that wrote the entry.
	Round       string        `json:"round,omitempty"`
	At          time.Time     `json:"at"`
	Entries     []JournalLine `json:"entries" tstype:"JournalLine[] | null"`
	NeedsAction bool          `json:"needsAction"`
}

// An item is one line of delegated work on a goal. State is open, done or
// dropped, and list order is the numeric item suffix (i2 before i10).
type Item struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	State     string    `json:"state"`
	Assignee  string    `json:"assignee,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Rule struct {
	ID         string    `json:"id"`
	Text       string    `json:"text"`
	Setter     string    `json:"setter"`
	SetterName string    `json:"setterName"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Edits      []Edit    `json:"edits,omitempty"`
}

type View struct {
	Rules []Rule `json:"rules" tstype:"Rule[] | null"`
	Used  int    `json:"used"`
	Max   int    `json:"max"`
	Hash  string `json:"hash"`
}
