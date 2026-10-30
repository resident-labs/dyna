package ghclient

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"dyna-agent/internal/approvals"
	"dyna-agent/internal/room"
)

// Verb names a GitHub write independently of its CLI spelling.
type Verb string
type Target string
type Item string
type RuleID string
type Reason string

const (
	TargetRoom      Target = "room"
	TargetUpstream  Target = "upstream"
	TargetOther     Target = "other"
	ItemNone        Item   = "none"
	ItemRoomAccount Item   = "room-account"
	ItemSessionPR   Item   = "session-pr"
	ItemForeign     Item   = "foreign"
)

const (
	ReasonReadOnly       Reason = "read-only-room"
	ReasonOther          Reason = "not-room-repository"
	ReasonBranchDelete   Reason = "upstream-branch-delete"
	ReasonForeignItem    Reason = "not-room-account-item"
	ReasonForeignComment Reason = "foreign-comment"
	ReasonForeignHead    Reason = "upstream-edit-foreign-head"
	ReasonUpstreamVerb   Reason = "upstream-verb-not-allowed"
)

// Write is the parsed command and its repository scope.
type Write struct {
	Verb         Verb
	Host, Repo   string
	Target       Target
	Ref          ItemRef
	Argv         []string
	API          bool
	DeleteBranch bool
	ValidItem    bool
}

// ParseWrite parses a gh command after its repository has been resolved.
func ParseWrite(args []string, host, repo string, target Target) Write {
	w := Write{Verb: "unknown", Host: host, Repo: repo, Target: target, Argv: slices.Clone(args)}
	a := commandArgs(args)
	if len(a) == 0 {
		return w
	}
	if a[0] == "api" {
		w.API = true
		return parseAPIWrite(w, a)
	}
	if len(a) < 2 {
		return w
	}
	p := parseArgs(a[:2], a[2:])
	w.Ref = ItemRef{Host: host, Repo: repo}
	switch a[0] + " " + a[1] {
	case "issue create", "issue edit", "issue comment", "issue close", "issue reopen",
		"pr create", "pr edit", "pr ready", "pr comment", "pr close", "pr reopen",
		"run cancel", "repo fork":
		w.Verb = Verb(a[0] + "." + a[1])
	case "pr review":
		w.Verb = "pr.review.verdict"
		if commentOnlyReview(p) {
			w.Verb = "pr.review.comment"
		}
	case "run rerun":
		w.Verb = "run.rerun"
		if v, ok := p.flags["--failed"]; ok {
			if v == "" {
				w.Verb = "run.rerun.failed"
			} else if enabled, err := strconv.ParseBool(v); err == nil && enabled {
				w.Verb = "run.rerun.failed"
			}
		}
	}
	switch a[0] {
	case "issue":
		w.Ref.Kind = ItemIssue
	case "pr":
		w.Ref.Kind = ItemPR
	case "run":
		w.Ref.Kind = ItemRun
	}
	w.ValidItem = len(p.positional) <= 1 && (len(p.positional) == 1 || w.Ref.Kind == ItemPR)
	if len(p.positional) == 1 {
		w.Ref.Target = p.positional[0]
	}
	_, longDelete := p.flags["--delete-branch"]
	_, shortDelete := p.flags["-d"]
	w.DeleteBranch = longDelete || shortDelete
	if w.Verb == "repo.fork" {
		if _, org := p.flags["--org"]; org || len(p.positional) > 1 {
			w.Verb = "unknown"
		}
	}
	return w
}

func parseAPIWrite(w Write, args []string) Write {
	call := parseAPICall(args)
	p := parseArgs(args[:1], args[1:])
	if call.graphql || len(p.positional) != 1 {
		return w
	}
	method := call.method
	if method == "" && call.fields {
		method = "POST"
	}
	endpoint, _, _ := strings.Cut(p.positional[0], "?")
	parts := strings.Split(strings.Trim(endpoint, "/"), "/")
	if len(parts) < 4 || parts[0] != "repos" {
		return w
	}
	// Repository scope was resolved from the endpoint at the boundary.
	if !strings.EqualFold(w.Repo, parts[1]+"/"+parts[2]) {
		return w
	}
	w.Ref = ItemRef{Host: w.Host, Repo: w.Repo}
	if c, ok := ownCommentCall(args); ok {
		w.Ref.Kind, w.Ref.Target, w.ValidItem = ItemComment, c.kind+"/comments/"+c.id, true
		if c.method == "PATCH" {
			w.Verb = "comment.edit"
		} else {
			w.Verb = "comment.delete"
		}
		return w
	}
	rest := parts[3:]
	if method == "POST" && len(rest) == 1 {
		switch rest[0] {
		case "issues":
			w.Verb = "issue.create"
		case "pulls":
			w.Verb = "pr.create"
		}
		return w
	}
	if method == "POST" && len(rest) >= 2 && rest[len(rest)-1] == "comments" {
		switch rest[0] {
		case "issues":
			w.Verb = "issue.comment"
		case "pulls":
			w.Verb = "pr.comment"
		case "commits":
			w.Verb = "issue.comment"
		}
		return w
	}
	if method == "POST" && len(rest) == 3 && rest[0] == "pulls" && rest[2] == "reviews" {
		if _, err := strconv.Atoi(rest[1]); err == nil && call.event == "COMMENT" && !call.input && !slices.ContainsFunc(call.keys, func(k string) bool { return strings.HasPrefix(k, "comments") }) {
			w.Verb = "pr.review.comment"
		} else {
			w.Verb = "pr.review.verdict"
		}
		return w
	}
	if method == "PATCH" && len(rest) == 2 && (rest[0] == "issues" || rest[0] == "pulls") {
		if _, err := strconv.Atoi(rest[1]); err != nil {
			return w
		}
		if rest[0] == "issues" {
			w.Ref.Kind = ItemIssue
		} else {
			w.Ref.Kind = ItemPR
		}
		w.Ref.Target, w.ValidItem = rest[1], true
		if call.input {
			return w
		}
		if slices.ContainsFunc(call.keys, func(k string) bool { return strings.HasPrefix(k, "state") }) {
			switch call.state {
			case "closed":
				w.Verb = Verb(string(w.Ref.Kind) + ".close")
			case "open":
				w.Verb = Verb(string(w.Ref.Kind) + ".reopen")
			}
			return w
		}
		w.Verb = Verb(string(w.Ref.Kind) + ".edit")
	}
	return w
}

type ruleCondition uint8

const (
	conditionAlways ruleCondition = iota
	conditionReadOnly
	conditionDeleteBranch
	conditionForeignHead
	conditionREST
)

// Rule is one row of the policy table. Empty matchers are wildcards.
type Rule struct {
	ID        RuleID
	Verbs     []Verb
	Targets   []Target
	Item      Item
	Class     approvals.Class
	Refusal   Reason
	condition ruleCondition
}

var writeRules = []Rule{
	{ID: "read-only", condition: conditionReadOnly, Refusal: ReasonReadOnly},
	{ID: "other-repository", Targets: []Target{TargetOther}, Refusal: ReasonOther},
	{ID: "upstream-branch-delete", Verbs: []Verb{"pr.close"}, Targets: []Target{TargetUpstream}, condition: conditionDeleteBranch, Refusal: ReasonBranchDelete},
	{ID: "upstream-edit-head", Verbs: []Verb{"pr.edit"}, Targets: []Target{TargetUpstream}, condition: conditionForeignHead, Refusal: ReasonForeignHead},
	{ID: "comment-foreign", Verbs: []Verb{"comment.edit", "comment.delete"}, Targets: []Target{TargetRoom, TargetUpstream}, Item: ItemForeign, Refusal: ReasonForeignComment},
	{ID: "item-foreign-upstream", Verbs: []Verb{"issue.close", "issue.reopen", "pr.close", "pr.reopen"}, Targets: []Target{TargetUpstream}, Item: ItemForeign, Refusal: ReasonForeignItem},
	{ID: "pr-create-api", Verbs: []Verb{"pr.create"}, Targets: []Target{TargetRoom}, condition: conditionREST, Class: approvals.ClassAgent},
	{ID: "pr-create-room", Verbs: []Verb{"pr.create"}, Targets: []Target{TargetRoom}, Class: approvals.ClassAuto},
	{ID: "pr-create-upstream", Verbs: []Verb{"pr.create"}, Targets: []Target{TargetUpstream}, Class: approvals.ClassAgent},
	{ID: "pr-own", Verbs: []Verb{"pr.edit", "pr.ready", "pr.comment"}, Targets: []Target{TargetRoom}, Item: ItemSessionPR, Class: approvals.ClassAuto},
	{ID: "pr-collaborate", Verbs: []Verb{"pr.edit", "pr.ready", "pr.comment"}, Targets: []Target{TargetRoom, TargetUpstream}, Class: approvals.ClassAgent},
	{ID: "review-comment", Verbs: []Verb{"pr.review.comment"}, Targets: []Target{TargetRoom, TargetUpstream}, Class: approvals.ClassAgent},
	{ID: "review-verdict", Verbs: []Verb{"pr.review.verdict"}, Targets: []Target{TargetRoom, TargetUpstream}, Class: approvals.ClassOwner},
	{ID: "issue-collaborate", Verbs: []Verb{"issue.create", "issue.edit", "issue.comment"}, Targets: []Target{TargetRoom, TargetUpstream}, Class: approvals.ClassAgent},
	{ID: "item-own", Verbs: []Verb{"issue.close", "issue.reopen", "pr.close", "pr.reopen"}, Targets: []Target{TargetRoom, TargetUpstream}, Item: ItemRoomAccount, Class: approvals.ClassAgent},
	{ID: "item-foreign-room", Verbs: []Verb{"issue.close", "issue.reopen", "pr.close", "pr.reopen"}, Targets: []Target{TargetRoom}, Item: ItemForeign, Class: approvals.ClassOwner},
	{ID: "comment-own", Verbs: []Verb{"comment.edit", "comment.delete"}, Targets: []Target{TargetRoom, TargetUpstream}, Item: ItemRoomAccount, Class: approvals.ClassAgent},
	{ID: "rerun-failed", Verbs: []Verb{"run.rerun.failed"}, Targets: []Target{TargetRoom}, Class: approvals.ClassAgent},
	{ID: "run-own", Verbs: []Verb{"run.rerun", "run.rerun.failed", "run.cancel"}, Targets: []Target{TargetRoom, TargetUpstream}, Item: ItemRoomAccount, Class: approvals.ClassAgent},
	{ID: "fork-upstream", Verbs: []Verb{"repo.fork"}, Targets: []Target{TargetUpstream}, Class: approvals.ClassAgent},
	{ID: "upstream-verb", Targets: []Target{TargetUpstream}, Refusal: ReasonUpstreamVerb},
	{ID: "room-owner", Targets: []Target{TargetRoom}, Class: approvals.ClassOwner},
}

type Decision struct {
	Class   approvals.Class
	Refusal Reason
	Rule    RuleID
}
type WriteLookups struct {
	Login    string
	AuthorOf func(context.Context, ItemRef) (string, error)
	OwnPR    func(context.Context, ItemRef) bool
	ForkHead func(context.Context, Write) bool
}

// RefusalError carries the exact policy row back to the caller.
type RefusalError struct {
	Write    Write
	Decision Decision
}

func (e *RefusalError) Error() string {
	var action string
	switch e.Decision.Refusal {
	case ReasonReadOnly:
		action = "ask the room owner to enable GitHub writes"
	case ReasonOther:
		action = "choose a repository registered to the room"
	case ReasonBranchDelete:
		action = "remove --delete-branch or -d"
	case ReasonForeignComment:
		action = "edit or delete only comments authored by the room account; the owner may edit other comments directly on GitHub"
	case ReasonForeignItem:
		action = "choose an item authored by the room account"
	case ReasonForeignHead:
		action = "choose a pull request whose head is in the room fork"
	case ReasonUpstreamVerb:
		action = "use a supported upstream operation or write to the room repository"
	}
	return fmt.Sprintf("GitHub write refused by rule %s (%s) for %s/%s; %s", e.Decision.Rule, e.Decision.Refusal, e.Write.Host, e.Write.Repo, action)
}

// Decide checks a write, using at most one author lookup.
func Decide(ctx context.Context, rec room.Record, w Write, lookups WriteLookups) (Decision, error) {
	item := ItemNone
	itemResolved := false
	for _, rule := range writeRules {
		if len(rule.Verbs) > 0 && !slices.Contains(rule.Verbs, w.Verb) {
			continue
		}
		if len(rule.Targets) > 0 && !slices.Contains(rule.Targets, w.Target) {
			continue
		}
		switch rule.condition {
		case conditionReadOnly:
			if Access(ctx, rec) == ReadWrite {
				continue
			}
		case conditionREST:
			if !w.API {
				continue
			}
		case conditionDeleteBranch:
			if !w.DeleteBranch {
				continue
			}
		case conditionForeignHead:
			if lookups.ForkHead != nil && lookups.ForkHead(ctx, w) {
				continue
			}
		}
		if rule.Item != "" && !itemResolved {
			itemResolved = true
			switch w.Verb {
			case "issue.close", "issue.reopen", "pr.close", "pr.reopen", "comment.edit", "comment.delete", "run.rerun", "run.rerun.failed", "run.cancel":
				item = ItemForeign
				if w.ValidItem && lookups.Login != "" && lookups.AuthorOf != nil {
					author, err := lookups.AuthorOf(ctx, w.Ref)
					if err == nil && strings.EqualFold(author, lookups.Login) {
						item = ItemRoomAccount
					}
				}
			case "pr.edit", "pr.ready", "pr.comment":
				if w.Target == TargetRoom && w.ValidItem && lookups.OwnPR != nil && lookups.OwnPR(ctx, w.Ref) {
					item = ItemSessionPR
				}
			}
		}
		if rule.Item != "" && rule.Item != item {
			continue
		}
		return Decision{Class: rule.Class, Refusal: rule.Refusal, Rule: rule.ID}, nil
	}
	return Decision{}, fmt.Errorf("invalid GitHub target %q", w.Target)
}
