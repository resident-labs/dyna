// Package guidance holds the one shipped Dyna orchestration skill.
package guidance

import _ "embed"

//go:embed skill_body.md
var Body string

const Description = "Load when the user asks for Dyna, a workflow, subagents, or multi-model orchestration; it points to dyna guide, the only source of Dyna guidance."
const Frontmatter = "---\nname: load-dyna-orchestrator\ndescription: " + Description + "\n---\n\n"
