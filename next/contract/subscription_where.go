package contract

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// EventFields are what a subscription's Where can name on an event. An event about many items, as one pass's change is, has one
// set for each thing in it, and a Where holds if one set meets all of it.
type EventFields struct {
	Repo   RepoName
	Number int
	Author string
	Actor  string
	Label  string
	Kind   string // one PrChangeKind of a change, such as pushed
	Text   string
}

func (f EventFields) value(key string) string {
	switch key {
	case "repo":
		return string(f.Repo)
	case "number":
		if f.Number == 0 {
			return ""
		}
		return strconv.Itoa(f.Number)
	case "author":
		return f.Author
	case "actor":
		return f.Actor
	case "label":
		return f.Label
	case "kind":
		return f.Kind
	}
	return f.Text
}

func itemFields(item GitHubItem) EventFields {
	fields := EventFields{Repo: item.Repo, Number: item.Number, Author: item.Author, Actor: item.Actor, Text: item.Text}
	if item.Action == GitHubLabeled || item.Action == GitHubUnlabeled {
		fields.Label = item.Detail
	}
	return fields
}

func changeFields(base EventFields, kinds []PrChangeKind, actions []GitHubItem, comments []PrComment) []EventFields {
	out := []EventFields{base}
	for _, kind := range kinds {
		fields := base
		fields.Kind = string(kind)
		out = append(out, fields)
	}
	for _, action := range actions {
		fields := itemFields(action)
		fields.Repo, fields.Number, fields.Author = base.Repo, base.Number, base.Author
		out = append(out, fields)
	}
	for _, comment := range comments {
		out = append(out, EventFields{Repo: base.Repo, Number: base.Number, Author: base.Author, Actor: comment.Author, Text: comment.Body})
	}
	return out
}

// Fields lists what a Where can see of an event.
func Fields(body EventBody) []EventFields {
	var out []EventFields
	switch b := body.(type) {
	case interface{ Item() GitHubItem }:
		out = append(out, itemFields(b.Item()))
	case CIFinished:
		out = append(out, EventFields{Repo: b.Repo, Number: b.Number})
	case GitMainUpdated:
		out = append(out, EventFields{Repo: b.Repo})
	case PrChanged:
		for _, change := range b.Changes {
			out = append(out, changeFields(EventFields{Repo: change.PR.Repo, Number: change.PR.Number, Author: change.PR.Author}, change.Kinds, change.Actions, change.Comments)...)
		}
		out = append(out, Fields(IssueChanged{Changes: b.Issues})...)
	case IssueChanged:
		for _, change := range b.Changes {
			out = append(out, changeFields(EventFields{Repo: change.Issue.Repo, Number: change.Issue.Number, Author: change.Issue.Author}, change.Kinds, change.Actions, change.Comments)...)
		}
	}
	return out
}

// whereClause is one parsed line of a Where.
type whereClause struct{ key, op, value string }

var whereKeys = []string{"repo", "number", "author", "actor", "label", "kind"}

func parseWhere(text string) (whereClause, error) {
	if value, ok := strings.CutPrefix(text, "text~"); ok && value != "" {
		return whereClause{"text", "~", value}, nil
	}
	key, value, ok := strings.Cut(text, "=")
	op := "="
	if strings.HasSuffix(key, "!") {
		key, op = strings.TrimSuffix(key, "!"), "!="
	}
	if !ok || value == "" || !slices.Contains(whereKeys, key) {
		return whereClause{}, fmt.Errorf("where %q is not %s=VALUE, the same with !=, or text~VALUE", text, strings.Join(whereKeys, "|"))
	}
	if _, err := strconv.Atoi(value); err != nil && key == "number" {
		return whereClause{}, fmt.Errorf("where %q needs a number", text)
	}
	return whereClause{key, op, value}, nil
}

// CheckWhere says why a line of a Where is no good, if it is not.
func CheckWhere(text string) error {
	_, err := parseWhere(text)
	return err
}

// holds tests a clause on one set of fields. A field the event does not have meets neither = nor !=.
func (c whereClause) holds(fields EventFields) bool {
	got := fields.value(c.key)
	switch {
	case got == "":
		return false
	case c.op == "~":
		return strings.Contains(got, c.value)
	}
	return (got == c.value) == (c.op == "=")
}

// MatchesWhere reports whether an event meets a Where, all of whose lines must hold of one thing in it. An empty Where always does.
func MatchesWhere(where []string, body EventBody) bool {
	if len(where) == 0 {
		return true
	}
	clauses := make([]whereClause, 0, len(where))
	for _, text := range where {
		clause, err := parseWhere(text)
		if err != nil {
			return false
		}
		clauses = append(clauses, clause)
	}
	return slices.ContainsFunc(Fields(body), func(fields EventFields) bool {
		return !slices.ContainsFunc(clauses, func(clause whereClause) bool { return !clause.holds(fields) })
	})
}
