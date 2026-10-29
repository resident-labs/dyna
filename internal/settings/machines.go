package settings

import (
	"fmt"
	"strconv"
	"strings"
)

// DefaultCIDiskFloorGB is the recovery floor a new settings file starts with.
// Twenty gigabytes covers a full local CI run with room to spare. The unit is
// GiB (2^30 bytes), and the key keeps the shorter GB name that owners read.
const DefaultCIDiskFloorGB = 20

// parseCIDiskFloorGB reads a whole number of gigabytes. Zero turns cache
// recovery off, so a run cleans no cache for space whatever its free space.
func parseCIDiskFloorGB(value string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a whole number of gigabytes, 0 or more", KeyCIDiskFloorGB)
	}
	return n, nil
}

// CIDiskFloorBytes is the recovery floor in bytes, the unit local CI works
// in. One step of the setting is one GiB (2^30 bytes).
func (v Values) CIDiskFloorBytes() uint64 { return uint64(v.CIDiskFloorGB) << 30 }
