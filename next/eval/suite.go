// Package eval defines the fixed agent-surface evaluation suite and scoring.
package eval

import "strings"

// Task identifies the model prompt and the operation expected at the trusted
// engine or box boundary. Fixture aliases are resolved before dispatch.
type Task struct {
	ID        int
	Prompt    string
	Operation Operation
}

var tasks = [...]Task{
	{1, "List the names of all sessions in this room.", "session.list"},
	{2, "List the names of all repositories in this room.", "room.repos"},
	{3, "Read the latest room posts and report the full text of the latest post from {{AName}}.", "events.list"},
	{4, "Send {{AName}} the message: Check complete.", "post"},
	{5, "Send {{AName}} and {{BName}} the message: Please review.", "post"},
	{6, "Start a child named eval-worker using profile {{Profile}}. Give it the brief: Report ready.", "session.start"},
	{7, "Change child {{A}} to profile {{Profile}}.", "session.set_profile"},
	{8, "Move {{C}} directly under {{Lead}}.", "session.move"},
	{9, "Close child {{B}} without discarding its archive.", "session.close"},
	{10, "Stop the current turn of child {{A}}.", "session.stop"},
	{11, "Show the full dyna_children context section and list every child name.", "context.snapshot"},
	{12, "Pause this room.", "room.pause"},
	{13, "Update visual post {{Visual}} to use the local HTML file {{File}}.", "post"},
	{14, "Withdraw my pending operation {{Operation}}.", "op.withdraw"},
	{15, "On {{Host}}, print the contents of the seeded file {{File}}.", "hostexec.run"},
	{16, "Request access to {{Host}} for the reason: inspect sandbox.", "op.submit"},
	{17, "Replace the stopped box of idle child {{A}}.", "session.restart"},
	{18, "Send {{AName}} the seeded file {{File}} as an attachment, with the message: Results attached.", "post"},
	{19, "Open {{Page}} in the box computer and tell me the page text.", "computer.text"},
	{20, "Click the Reveal button on the open sandbox page and report the code.", "computer.input"},
}

// Tasks returns an independent copy of the fixed task list.
func Tasks() []Task { return append([]Task(nil), tasks[:]...) }

// Prompt resolves fixture identities without replacing ordinary words in the task.
func (t Task) PromptFor(f Fixture) string {
	prompt := t.Prompt
	if t.ID == 11 && f.Task11Fallback {
		prompt = "List the room log filtered to visual posts and report the title of the seeded visual post."
	}
	return strings.NewReplacer(
		"{{A}}", f.A, "{{B}}", f.B, "{{AName}}", f.AName, "{{BName}}", f.BName, "{{C}}", f.C, "{{Lead}}", f.Lead,
		"{{Profile}}", f.Profile, "{{Operation}}", f.Operation, "{{Host}}", f.Host,
		"{{File}}", f.File, "{{Visual}}", f.Visual, "{{Page}}", f.Page,
	).Replace(prompt)
}
