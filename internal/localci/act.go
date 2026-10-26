package localci

import (
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
// structure. A document without a name key gains one in the parsed mapping.
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
		// Replacing an anchored value drops the anchor, and an alias elsewhere
		// in the file that points at it would then name nothing, which act's
		// decoder rejects. Each alias becomes a copy of the original value
		// first, so every other field keeps the value it had.
		if anchor, ok := entry.Value.(*ast.AnchorNode); ok {
			if err := inlineAlias(file.Docs[0], anchor); err != nil {
				return nil, err
			}
		}
		if err := entry.Replace(tagName.Value); err != nil {
			return nil, fmt.Errorf("replace the name value: %w", err)
		}
		return []byte(file.String()), nil
	}
	// No name key: the name joins the parsed mapping, ahead of the other keys.
	// Text in front of the content cannot do this, because a comment, a
	// directive or a document marker before the mapping would leave the tag in
	// a document of its own.
	// The node comes from text at column 1, and the printer keeps every node's
	// own column, so the name moves to the column the mapping's keys start at.
	if len(body.Values) > 0 {
		tagName.AddColumn(body.Values[0].Key.GetToken().Position.Column - tagName.Key.GetToken().Position.Column)
	}
	body.Values = append([]*ast.MappingValueNode{tagName}, body.Values...)
	return []byte(file.String()), nil
}

// inlineAlias replaces every alias of anchor in the document with a copy of the
// anchored value. The value is read back from its text, so each copy is a node
// of its own.
func inlineAlias(root ast.Node, anchor *ast.AnchorNode) error {
	name := anchor.Name.String()
	text := anchor.Value.String()
	var failed error
	copyOf := func(at ast.Node) ast.Node {
		doc, err := parser.ParseBytes([]byte("k: "+text), 0)
		if err != nil {
			failed = fmt.Errorf("copy the anchored value: %w", err)
			return at
		}
		value := doc.Docs[0].Body.(*ast.MappingNode).Values[0].Value
		value.AddColumn(at.GetToken().Position.Column - value.GetToken().Position.Column)
		return value
	}
	var visit func(node ast.Node) ast.Node
	visit = func(node ast.Node) ast.Node {
		switch n := node.(type) {
		case *ast.AliasNode:
			if n.Value.String() == name {
				return copyOf(n)
			}
		case *ast.DocumentNode:
			n.Body = visit(n.Body)
		case *ast.MappingNode:
			for _, entry := range n.Values {
				visit(entry)
			}
		case *ast.MappingValueNode:
			n.Value = visit(n.Value)
		case *ast.SequenceNode:
			for i, value := range n.Values {
				n.Values[i] = visit(value)
			}
			for _, entry := range n.Entries {
				entry.Value = visit(entry.Value)
			}
		case *ast.AnchorNode:
			n.Value = visit(n.Value)
		case *ast.TagNode:
			n.Value = visit(n.Value)
		}
		return node
	}
	visit(root)
	return failed
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
