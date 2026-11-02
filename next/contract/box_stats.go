package contract

const (
	MethodBoxStats       Method = "box.stats"
	MethodBoxStatsReport Method = "box.stats.report"
)

// BoxStatsRequest reads what a session's box is using. Only the owner can, and a page asks only while it shows the box:
// nothing polls boxes in the background, and no reading is stored.
type BoxStatsRequest struct {
	Session SessionID `json:"session"`
}

// BoxStatsResponse has no Reading while the box is not running, or when its agent did not answer in time. Size holds the
// limits, because a box cannot see them: they sit on its slice, above its cgroup namespace.
type BoxStatsResponse struct {
	Size    BoxSize     `json:"size"`
	Reading *BoxReading `json:"reading,omitempty"`
}

// BoxReading is what a box agent reads about itself. CPUUsed is in cores, on average since its last reading. DiskUsed is
// the bytes in the box's /box volume and can be minutes old; it stays zero until the first measure ends.
type BoxReading struct {
	MemUsed  int64   `json:"memUsed"`
	CPUUsed  float64 `json:"cpuUsed"`
	DiskUsed int64   `json:"diskUsed,omitempty"`
}

// BoxStatsAsk is what the engine sends a box agent for a BoxStatsRequest. The agent answers with box.stats.report.
type BoxStatsAsk struct {
	Ask uint64 `json:"ask"`
}

// BoxStatsReportRequest answers a BoxStatsAsk.
type BoxStatsReportRequest struct {
	Ask     uint64     `json:"ask"`
	Reading BoxReading `json:"reading"`
}
type BoxStatsReportResponse struct{}

func (BoxStatsAsk) BoxMessageType() BoxMessageType { return "box.stats.ask" }
func (BoxStatsAsk) isBoxMessageBody()              {}
