package settings

import (
	"fmt"
	"strconv"
	"strings"
)

// DefaultCIDiskFloorGB is the floor a new settings file starts with. Twenty
// gigabytes covers a full local CI run with room to spare.
const DefaultCIDiskFloorGB = 20

// parseCIDiskFloorGB reads a whole number of gigabytes. Zero turns the floor
// off, so a machine takes CI work whatever its free space.
func parseCIDiskFloorGB(value string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a whole number of gigabytes, 0 or more", KeyCIDiskFloorGB)
	}
	return n, nil
}

// CIDiskFloorBytes is the disk floor in bytes, the unit a probe and the
// scheduler work in.
func (v Values) CIDiskFloorBytes() uint64 { return uint64(v.CIDiskFloorGB) << 30 }
