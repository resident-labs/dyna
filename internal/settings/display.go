package settings

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var sizeInput = regexp.MustCompile(`(?i)^((?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?)\s*(b|bytes?|[kmgtpe](?:i?b)?)?$`)

// ParseBytes accepts byte counts and binary units. MB and m both mean MiB.
// Rational arithmetic rejects fractional bytes and overflow without rounding.
func ParseBytes(value string) (int64, error) {
	invalid := func() (int64, error) {
		return 0, fmt.Errorf("size must be positive whole bytes, such as 25 MiB, 25MB, 25m or 26214400 (MB means MiB)")
	}
	parts := sizeInput.FindStringSubmatch(strings.TrimSpace(value))
	if parts == nil {
		return invalid()
	}
	n, ok := new(big.Rat).SetString(strings.ReplaceAll(parts[1], ",", ""))
	if !ok {
		return invalid()
	}
	unit := strings.ToLower(parts[2])
	power := 0
	if unit != "" && unit != "b" && unit != "byte" && unit != "bytes" {
		power = strings.IndexByte("kmgtpe", unit[0]) + 1
	}
	n.Mul(n, new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), uint(10*power))))
	if !n.IsInt() || !n.Num().IsInt64() || n.Sign() <= 0 {
		return invalid()
	}
	return n.Num().Int64(), nil
}

// FormatBytes uses the largest binary unit exact with at most two decimals.
// Other values use the exact byte count with thousands separators.
func FormatBytes(n int64) string {
	units := []string{"bytes", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	for i := len(units) - 1; i > 0; i-- {
		scale := int64(1) << (10 * i)
		if n < scale || n%(scale/4) != 0 {
			continue
		}
		value := new(big.Rat).SetFrac(big.NewInt(n), big.NewInt(scale)).FloatString(2)
		return strings.TrimRight(strings.TrimRight(value, "0"), ".") + " " + units[i]
	}
	digits := strconv.FormatInt(n, 10)
	sign := ""
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return sign + digits + " bytes"
}

// FormatDuration omits zero components while retaining Go duration input syntax.
func FormatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return strings.ReplaceAll(s, "h0m", "h")
}
