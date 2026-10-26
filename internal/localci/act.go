package localci

import (
	"path"
	"strconv"

	"dyna-agent/internal/shellquote"
)

// actVersion is the nektos/act release Dyna installs on a CI node. Dyna pins
// the release and the checksum, the way it pins a harness bundle: a node runs
// the binary Dyna chose, never whatever "act" is on its PATH.
const actVersion = "0.2.89"

// actDigest is the sha256 of act_Linux_x86_64.tar.gz for actVersion.
const actDigest = "0191d6f1f3b716b5c55820032605d05fc3c1cdbf581ebeff655019e5dd1524c0"

// actURL is the release asset this platform installs.
const actURL = "https://github.com/nektos/act/releases/download/v" + actVersion + "/act_Linux_x86_64.tar.gz"

// actBinaryPath is where a node keeps the pinned act binary.
func actBinaryPath(base string) string {
	return path.Join(actDir(base), "act")
}

func actDir(base string) string {
	return path.Join(base, "tools", "act-"+actVersion)
}

// installActScript installs the pinned act on the node once. Checks run beside
// each other, so the install owns a lock (descriptor 8, never Dyna's slot 9)
// and the others wait for it. A download or checksum failure exits 69, which
// the runner reports as a machine error rather than a failing test.
func installActScript(base string) string {
	q := shellquote.Quote
	dir := actDir(base)
	tarball := path.Join(dir, "act.tgz")
	return "mkdir -p " + q(dir) + "; exec 8>" + q(dir+".lock") + "; flock 8; " +
		"if [ ! -x " + q(actBinaryPath(base)) + " ]; then " +
		"curl -fsSL -o " + q(tarball) + " " + q(actURL) + " || exit 69; " +
		"printf '%s  %s\\n' " + q(actDigest) + " " + q(tarball) + " | sha256sum -c - >/dev/null || exit 69; " +
		"tar -xzf " + q(tarball) + " -C " + q(dir) + " act || exit 69; " +
		"chmod +x " + q(actBinaryPath(base)) + " || exit 69; " +
		"fi; exec 8>&-; "
}

// actContainerOptions are the limits and the label act gives every container
// it starts for one check.
func actContainerOptions(check nodeCheck, runID string) string {
	return "--cpus " + strconv.Itoa(check.resources.CPUs) +
		" --memory " + strconv.FormatInt(check.resources.MemoryMiB, 10) + "m" +
		" --label dyna.run=" + runID
}
