package eval

import "fmt"

// Fixture contains the isolated engine and box identities resolved at setup.
// Fields in Observations use these same canonical names and values.
type Fixture struct {
	Room, Lead, A, B, C, AName, BName, Profile string
	Operation, Host, File, Visual, Page        string
	ReadValue, FileSHA256                      string
	Task11Fallback                             bool
}

// Validate rejects incomplete fixtures before a model can act.
func (f Fixture) Validate() error {
	for name, value := range map[string]string{
		"room": f.Room, "lead": f.Lead, "A": f.A, "B": f.B, "C": f.C,
		"AName": f.AName, "BName": f.BName, "profile": f.Profile,
		"operation": f.Operation, "host": f.Host, "file": f.File, "visual": f.Visual,
		"page": f.Page, "readValue": f.ReadValue, "fileSHA256": f.FileSHA256,
	} {
		if value == "" {
			return fmt.Errorf("fixture %s is empty", name)
		}
	}
	return nil
}

func field(name FieldName, value string) Field { return Field{Name: name, Value: value} }

// Expected returns the required calls and observed facts for one frozen task.
// The fixture observer obtains write state from the engine or box. Read tasks
// use a unique fixture value that appears in the final answer.
func Expected(id int, f Fixture) (Expectation, error) {
	call := func(op Operation, args ...Field) Call { return Call{Operation: op, Arguments: args} }
	switch id {
	case 1:
		return Expectation{ReadValue: f.ReadValue, Calls: []Call{call("session.list", field("room", f.Room))}}, nil
	case 2:
		return Expectation{ReadValue: f.ReadValue, Calls: []Call{call("room.repos", field("room", f.Room))}}, nil
	case 3:
		return Expectation{ReadValue: f.ReadValue, Calls: []Call{call("events.list", field("room", f.Room))}}, nil
	case 4:
		return Expectation{Calls: []Call{call("post", field("recipient", f.A), field("text", "Check complete."))}, State: []Field{field("posts", "1"), field("recipient", f.A)}}, nil
	case 5:
		return Expectation{Calls: []Call{call("post", field("recipient.A", f.A), field("recipient.B", f.B), field("text", "Please review."))}, State: []Field{field("posts", "1"), field("recipient.A", f.A), field("recipient.B", f.B)}}, nil
	case 6:
		return Expectation{Calls: []Call{call("session.start", field("parent", f.Lead), field("name", "eval-worker"), field("profile", f.Profile), field("brief", "Report ready."))}, State: []Field{field("children.created", "1"), field("child.parent", f.Lead), field("child.name", "eval-worker"), field("child.profile", f.Profile), field("child.brief", "Report ready.")}}, nil
	case 7:
		return Expectation{Calls: []Call{call("session.set_profile", field("session", f.A), field("profile", f.Profile))}, State: []Field{field("session.profile", f.Profile)}}, nil
	case 8:
		return Expectation{Calls: []Call{call("session.move", field("session", f.C), field("parent", f.Lead))}, State: []Field{field("session.parent", f.Lead)}}, nil
	case 9:
		return Expectation{Calls: []Call{call("session.close", field("session", f.B), field("discard", "false"))}, State: []Field{field("session.closed", "true"), field("archive.discarded", "false")}}, nil
	case 10:
		return Expectation{Calls: []Call{call("session.stop", field("session", f.A))}, State: []Field{field("turn.stopped", "true")}}, nil
	case 11:
		if f.Task11Fallback {
			return Expectation{ReadValue: f.ReadValue, Calls: []Call{call("room.log", field("types", "visual"))}}, nil
		}
		return Expectation{ReadValue: f.ReadValue, Calls: []Call{call("context.snapshot", field("section", "dyna_children"))}}, nil
	case 12:
		return Expectation{Calls: []Call{call("room.pause", field("room", f.Room), field("paused", "true"))}, State: []Field{field("room.paused", "true")}}, nil
	case 13:
		return Expectation{Calls: []Call{call("post", field("visualUpdate", f.Visual), field("html", f.File))}, State: []Field{field("visual.updated", f.Visual), field("visual.source", f.File)}}, nil
	case 14:
		return Expectation{Calls: []Call{call("op.withdraw", field("operation", f.Operation))}, State: []Field{field("operation.withdrawn", "true")}}, nil
	case 15:
		return Expectation{ReadValue: f.ReadValue, Calls: []Call{call("hostexec.run", field("host", f.Host), field("file", f.File))}}, nil
	case 16:
		return Expectation{Calls: []Call{call("op.submit", field("kind", "hostexec.grant"), field("host", f.Host), field("reason", "inspect sandbox"))}, State: []Field{field("operations.created", "1"), field("operation.host", f.Host), field("operation.reason", "inspect sandbox")}}, nil
	case 17:
		return Expectation{Calls: []Call{call("session.restart", field("session", f.A))}, State: []Field{field("child.box.replaced", "true"), field("child.state", "idle")}}, nil
	case 18:
		return Expectation{Calls: []Call{call("post", field("recipient", f.A), field("text", "Results attached."))}, State: []Field{field("posts", "1"), field("recipient", f.A), field("text", "Results attached."), field("attachments", "1"), field("attachment.sha256", f.FileSHA256)}}, nil
	case 19:
		return Expectation{ReadValue: f.ReadValue, Calls: []Call{call("computer.open", field("url", f.Page)), call("computer.text")}}, nil
	case 20:
		return Expectation{ReadValue: f.ReadValue, Calls: []Call{call("computer.input", field("action", "click"))}}, nil
	default:
		return Expectation{}, fmt.Errorf("unknown evaluation task %d", id)
	}
}
