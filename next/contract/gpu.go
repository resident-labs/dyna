package contract

import (
	"fmt"
	"strconv"
)

// GPUGiB is the VRAM a box asks of a card, in whole GiB, or 0 for no GPU. On the wire `true` is 1 GiB and `false` is none; it
// decodes here once, so nothing past the boundary sees a boolean. GPU memory has no cgroup limit, so it is a request that
// admission counts and nothing enforces (5.5).
type GPUGiB int

func (g *GPUGiB) UnmarshalJSON(data []byte) error {
	switch text := string(data); text {
	case "true":
		*g = 1
	case "false", "null":
		*g = 0
	default:
		n, err := strconv.Atoi(text)
		if err != nil || n < 0 {
			return fmt.Errorf("gpu is a whole number of GiB, or true for 1 GiB: %s", text)
		}
		*g = GPUGiB(n)
	}
	return nil
}

// GPU is one card of a host. RequestedBytes is the VRAM that boxes placed on it have asked for, filled where hosts are listed.
type GPU struct {
	Index          int    `json:"index"`
	Name           string `json:"name"`
	MemBytes       int64  `json:"memBytes"`
	RequestedBytes int64  `json:"requestedBytes,omitempty"`
}
