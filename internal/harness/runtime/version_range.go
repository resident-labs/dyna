package runtime

import (
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// supportedVersionRange reports whether one exact version is inside the range
// Dyna's verified recipes support for the harness. reason is fixed user-facing
// text and is empty when the version is supported. A harness with no entry
// accepts any exact version: its recipes have no verified incompatibility yet.
// T3 Code keeps the same kind of per-provider compatibility ranges.
func supportedVersionRange(harness, version string) (bool, string) {
	switch harness {
	case "pi":
		if piForkLevel(version) >= minimumPiForkLevel {
			return true, ""
		}
		return false, "Dyna supports Pi " + piForkVersion + " or later of Dyna's Pi fork; upstream npm Pi and older forks lack features Dyna needs. Update Pi from Dyna's Harnesses page."
	case "kimi-code":
		if versionAtLeast(version, "2.3.1") {
			return true, ""
		}
		return false, "Dyna supports Kimi Code 2.3.1 or later."
	}
	return true, ""
}

// piForkLevel reads the Dyna fork level from a version like 1.4.0-dyna.3.
// It returns 0 for every other version, including upstream npm Pi, whose
// releases carry no fork features.
func piForkLevel(version string) int {
	_, suffix, ok := strings.Cut(version, "-dyna.")
	if !ok {
		return 0
	}
	level, err := strconv.Atoi(suffix)
	if err != nil || level < 1 {
		return 0
	}
	return level
}

// versionAtLeast compares exact versions. Semantic versions compare by
// precedence, except a Dyna fork prerelease satisfies its upstream base
// version. Vendor date formats that semver rejects compare as strings.
func versionAtLeast(version, min string) bool {
	if semver.IsValid("v"+version) && semver.IsValid("v"+min) {
		if semver.Compare("v"+version, "v"+min) >= 0 {
			return true
		}
		base, suffix, fork := strings.Cut(version, "-")
		return fork && strings.HasPrefix(suffix, "dyna.") && semver.Compare("v"+base, "v"+min) >= 0
	}
	return version >= min
}
