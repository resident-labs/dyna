package eval

import "encoding/hex"

// HashRecord is metadata only. Generation is set by a verified join to the
// Pi compaction record; nil means the join is missing or ambiguous.
type HashRecord struct {
	Session     string
	Provider    Provider
	Ordinal     int
	ToolsSHA256 string
	Generation  *int
}

// HashCheck separates an observed mismatch from missing comparison evidence.
type HashCheck struct {
	Compared   int
	Mismatches int
	Unmeasured bool
}

// CompareToolArrays checks consecutive requests for one session and provider.
// It refuses ambiguous ordering and compaction joins instead of guessing.
func CompareToolArrays(records []HashRecord) HashCheck {
	result := HashCheck{}
	if len(records) < 2 {
		result.Unmeasured = true
		return result
	}
	for i, current := range records {
		_, hashErr := hex.DecodeString(current.ToolsSHA256)
		if current.Session == "" || current.Provider == "" || current.Ordinal < 1 || len(current.ToolsSHA256) != 64 || hashErr != nil || current.Generation == nil {
			result.Unmeasured = true
			continue
		}
		if i == 0 {
			if current.Ordinal != 1 {
				result.Unmeasured = true
			}
			continue
		}
		previous := records[i-1]
		if previous.Session != current.Session || previous.Provider != current.Provider || previous.Ordinal+1 != current.Ordinal ||
			previous.Generation == nil || len(previous.ToolsSHA256) != 64 {
			result.Unmeasured = true
			continue
		}
		if *current.Generation < *previous.Generation {
			result.Unmeasured = true
			continue
		}
		if *previous.Generation != *current.Generation {
			continue
		}
		result.Compared++
		if previous.ToolsSHA256 != current.ToolsSHA256 {
			result.Mismatches++
		}
	}
	if result.Compared == 0 {
		result.Unmeasured = true
	}
	return result
}
