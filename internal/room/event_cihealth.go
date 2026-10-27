package room

// SlowTestEvent reports the tests that newly crossed the CI limit. It is a
// quiet owner-feed line; the PR watcher files it and nobody wakes.
type SlowTestEvent struct {
	// Run is the CI run token, such as "1241/2".
	Run string `json:"run"`
	// Count is how many tests crossed the limit in this run.
	Count     int                `json:"count"`
	Crossings []SlowTestCrossing `json:"crossings"`
	// Detail is the one line per crossing that the room log reads.
	Detail string `json:"detail,omitempty"`
}

// SlowTestCrossing is one test that crossed the limit, with both of its times.
type SlowTestCrossing struct {
	Test       string   `json:"test"`
	Package    string   `json:"package,omitempty"`
	Features   []string `json:"features,omitempty"`
	DurationMS int      `json:"durationMs"`
	Run        string   `json:"run"`
	OtherMS    int      `json:"otherMs"`
	OtherRun   string   `json:"otherRun"`
	LimitMS    int      `json:"limitMs"`
}

func (SlowTestEvent) eventFamily() EventFamily { return FamilyCIHealth }
func (SlowTestEvent) eventType() EventType     { return TypeSlowTest }
func (s SlowTestEvent) eventSubject() string   { return s.Run }
func (s SlowTestEvent) eventDedupe() string    { return s.Run }
