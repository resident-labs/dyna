package localci

import (
	"bytes"
	"fmt"
	"path"
	"strconv"

	"dyna-agent/internal/shellquote"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
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
	// Two labels: the run one finds every container a run started, the check one
	// finds exactly this check's, so one check's cleanup never removes a sibling
	// check's containers.
	return "--cpus " + strconv.Itoa(check.resources.CPUs) +
		" --memory " + strconv.FormatInt(check.resources.MemoryMiB, 10) + "m" +
		" --label dyna.run=" + runID +
		" --label dyna.check=" + string(check.id)
}

// tagWorkflowBody returns content with its top-level name value replaced by
// tag. act names a job's containers after the workflow, so two act processes
// that share one workflow fight over one container name, and every job check
// tags its own copy of the workflow it runs. The name is replaced through the
// parsed document, because a valid name takes forms a line edit corrupts: a
// quoted key gains a duplicate name, a block scalar keeps its content behind
// the replaced line, and a value on the line after its key never matches a
// same-line pattern at all. Everything outside the name value keeps its
// structure. A document without a name key provably cannot gain a duplicate,
// so the tag is prepended to it, behind a document marker if there is one.
func tagWorkflowBody(content []byte, tag string) ([]byte, error) {
	file, err := parser.ParseBytes(content, 0)
	if err != nil {
		return nil, fmt.Errorf("parse workflow: %w", err)
	}
	if len(file.Docs) != 1 {
		return nil, fmt.Errorf("the workflow is not one document")
	}
	body, ok := file.Docs[0].Body.(*ast.MappingNode)
	if !ok {
		return nil, fmt.Errorf("the workflow is not a mapping")
	}
	// A node parsed from the tag itself replaces the value, so no node is ever
	// built by hand and the value renders as the plain scalar act reads.
	tagged, err := parser.ParseBytes([]byte("name: "+tag), 0)
	if err != nil {
		return nil, err
	}
	tagName := tagged.Docs[0].Body.(*ast.MappingNode).Values[0]
	for _, entry := range body.Values {
		key, ok := entry.Key.(*ast.StringNode)
		if !ok || key.Value != "name" {
			continue
		}
		if err := entry.Replace(tagName.Value); err != nil {
			return nil, fmt.Errorf("replace the name value: %w", err)
		}
		return []byte(file.String()), nil
	}
	if bytes.HasPrefix(content, []byte("---\n")) {
		return append([]byte("---\nname: "+tag+"\n"), content[4:]...), nil
	}
	return append([]byte("name: "+tag+"\n"), content...), nil
}

// actArtifactPortBase is the first port the artifact server of a node's slot
// uses. Each slot takes its own port, so two checks never share one.
const actArtifactPortBase = 34000

// matrixFlags selects one configuration of a matrix job, so a workflow shard is
// its own check.
func matrixFlags(entries []string) string {
	out := ""
	for _, entry := range entries {
		out += " --matrix " + shellquote.Quote(entry)
	}
	return out
}
