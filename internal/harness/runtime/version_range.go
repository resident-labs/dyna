package runtime

import (
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
		if versionAtLeast(version, "1.1.0") {
			return true, ""
		}
		return false, "The " + piNPMPackage + " package starts at Pi 1.1.0."
	case "kimi-code":
		if versionAtLeast(version, "2.3.1") {
			return true, ""
		}
		return false, "Dyna supports Kimi Code 2.3.1 or later."
	}
	return true, ""
}

// versionAtLeast compares exact versions. Semantic versions compare by
// precedence; vendor date formats that semver rejects compare as strings.
func versionAtLeast(version, min string) bool {
	if semver.IsValid("v"+version) && semver.IsValid("v"+min) {
		return semver.Compare("v"+version, "v"+min) >= 0
	}
	return version >= min
}
