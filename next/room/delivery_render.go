package room

import (
	"fmt"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// RenderDelivery renders the event bodies that may be delivered to a session.
func RenderDelivery(body contract.EventBody) (string, error) {
	switch b := body.(type) {
	case contract.SessionProfileChanged:
		return fmt.Sprintf("Your model is now %s.", b.Profile), nil
	case contract.PostCreated:
		return b.Text, nil
	case contract.SessionRestarted:
		switch {
		case b.ImageUpdated:
			return withContinue("Your box restarted on a new image. Files under /box and this conversation are kept; anything installed outside /box is gone.", b.OpenGoal), nil
		case b.Refreshed:
			return withContinue("Your box restarted on the current version. Files under /box and this conversation are kept.", b.OpenGoal), nil
		case b.Stopped:
			return withContinue("Your box restarted after it stopped. Files under /box and this conversation are kept; any process that was running in it is gone.", b.OpenGoal), nil
		}
		return withContinue("Your box was recreated after it failed. This conversation is restored, but the old volume is gone: files under /box that you did not push are lost.", b.OpenGoal), nil
	case contract.SessionStarted:
		var parts []string
		if b.Goal != "" {
			parts = append(parts, "Goal: "+b.Goal)
		}
		if b.DoneWhen != "" {
			parts = append(parts, "Done when: "+b.DoneWhen)
		}
		if b.Brief != "" {
			parts = append(parts, b.Brief)
		}
		return strings.Join(parts, "\n\n"), nil
	case contract.GoalSet:
		text := fmt.Sprintf("Goal %s is yours: %s", b.Goal, b.Text)
		if b.DoneWhen != "" {
			text += "\n\nDone when: " + b.DoneWhen
		}
		return text, nil
	case contract.CheckinDue:
		return renderCheckin(b), nil
	case contract.SessionFailed:
		return renderFailures(b), nil
	case contract.SessionClosed:
		var did []string
		if len(b.Handed) > 0 {
			did = append(did, "handed you "+ResponsibilityNames(b.Handed))
		}
		if len(b.Unheld) > 0 {
			did = append(did, "left goals with no holder, because you are root and root holds no goals: "+GoalNames(b.Unheld)+". goal list with your session shows them; goal done ends them and settles their open items")
		}
		return fmt.Sprintf("Session %s closed and %s.", b.Session, strings.Join(did, " and ")), nil
	case contract.EngineRestarted:
		build, previous := b.Build[:min(12, len(b.Build))], b.PreviousBuild[:min(12, len(b.PreviousBuild))]
		if !b.Clean {
			return fmt.Sprintf("Dyna restarted on build %s after an unclean stop (was %s).", build, previous), nil
		}
		if b.Build != b.PreviousBuild {
			return fmt.Sprintf("Dyna restarted on build %s (was %s); it was down %d s.", build, previous, b.DownMillis/1000), nil
		}
		return fmt.Sprintf("Dyna restarted on build %s; it was down %d s.", build, b.DownMillis/1000), nil
	case contract.HostDiskLow:
		return fmt.Sprintf("Host %s is low on disk: %d GiB free, %d GiB wanted. At the floor, new boxes wait and the oldest stopped boxes close. Closing a finished session frees its volume.", b.Host, b.FreeBytes>>30, b.FloorBytes>>30), nil
	case contract.PrOpened:
		if b.Actor == "" {
			return fmt.Sprintf("PR opened: %s", b.URL), nil
		}
		return renderGitHubItem(b.Item()), nil
	case contract.QuestionAnswered:
		return fmt.Sprintf("Question: %s\n\nAnswer: %s", b.Question, b.Answer), nil
	case contract.PrChanged:
		return renderPRChanged(b), nil
	case contract.IssueChanged:
		return strings.Join(issueBlocks(b.Changes), "\n\n"), nil
	case contract.IssueClosedEvent:
		return fmt.Sprintf("Issue %s#%d closed%s. %s", b.Repo, b.Number, by(b.Actor), b.URL), nil
	case contract.PrMergedEvent:
		return fmt.Sprintf("PR %s#%d merged%s. %s", b.Repo, b.Number, by(b.Actor), b.URL), nil
	case contract.PrClosedEvent:
		return fmt.Sprintf("PR %s#%d closed without merging%s. %s", b.Repo, b.Number, by(b.Actor), b.URL), nil
	case contract.IssueOpenedEvent, contract.GitHubComment, contract.GitHubLabel, contract.GitHubMention:
		return renderGitHubItem(b.(interface{ Item() contract.GitHubItem }).Item()), nil
	case contract.CIFinished:
		return fmt.Sprintf("CI on PR %s#%d finished: %s. %s", b.Repo, b.Number, b.Result, b.URL), nil
	case contract.GitMainUpdated:
		return fmt.Sprintf("The default branch of %s moved to %s.", b.Repo, b.Head), nil
	case contract.GitMainCIFinished:
		text := fmt.Sprintf("CI on %s %s at %s finished: %s.", b.Repo, b.Branch, b.Sha, b.Result)
		for _, run := range b.Runs {
			text += fmt.Sprintf("\n%s: %s %s", run.Name, run.Conclusion, run.URL)
		}
		return text, nil
	case contract.NoticePosted:
		return b.Text, nil
	case contract.SessionNotice:
		return b.Text, nil
	case contract.BoxChanged:
		return fmt.Sprintf("Your workspace could not start on %s after two failures. Dyna is still retrying. Start error: %s", b.Host, b.Reason), nil
	case contract.HookDue:
		return fmt.Sprintf("Subscription %s is due.", b.Subscription), nil
	case contract.HookFinished:
		return fmt.Sprintf("Subscription %s finished: %s, exit code %d. Last output:\n%s", b.Subscription, b.Result, b.ExitCode, contract.Outside("subscription "+string(b.Subscription), "", b.Output)), nil
	case contract.OpDecided:
		return fmt.Sprintf("Operation %s: %s. %s", b.ID, b.State, b.Decision.Reason), nil
	case contract.OpRan:
		return fmt.Sprintf("Operation %s: %s. %s\n%s", b.ID, b.State, b.Result.Error, b.Result.Output), nil
	case contract.TurnEnded:
		if b.Outcome == contract.TurnFailed {
			return "turn failed: " + b.Error, nil
		}
		return b.Final, nil
	case contract.ShellJobRunning:
		text := fmt.Sprintf("Your shell job %s has been running for %s.", jobName(b.Job, b.Description), jobTime(b.RanMinutes))
		switch {
		case b.QuietMinutes == nil:
			return text + " It has written no output.", nil
		case b.Tail == "":
			return fmt.Sprintf("%s Last output %s ago.", text, jobTime(*b.QuietMinutes)), nil
		}
		return fmt.Sprintf("%s Last output %s ago:\n%s", text, jobTime(*b.QuietMinutes), b.Tail), nil
	case contract.ShellJobEnded:
		result := jobExit(b.End.Code)
		switch b.End.Kind {
		case contract.ShellJobSignaled:
			result = fmt.Sprintf("ended by a signal (%s)", b.End.Signal)
		case contract.ShellJobStopped:
			result = "was stopped"
		}
		return fmt.Sprintf("Your shell job %s %s. Last output:\n%s", jobName(b.Job, b.Description), result, b.Tail), nil
	default:
		return "", fmt.Errorf("event body %T cannot be delivered to a session", body)
	}
}

var prChangeWords = map[contract.PrChangeKind]string{
	contract.PrChangeCIFailed:         "CI failed",
	contract.PrChangeCIPassed:         "CI passed",
	contract.PrChangeChangesRequested: "changes requested",
	contract.PrChangeApproved:         "approved",
	contract.PrChangeConflict:         "conflicts with its base",
	contract.PrChangeMerged:           "merged",
	contract.PrChangeClosed:           "closed without merging",
	contract.PrChangeComment:          "new comments",
	contract.PrChangePushed:           "pushed",
}

var issueChangeWords = map[contract.PrChangeKind]string{
	contract.PrChangeClosed:  "closed",
	contract.PrChangeComment: "new comments",
}

func by(actor string) string {
	if actor == "" {
		return ""
	}
	return " by @" + actor
}

// renderGitHubItem shows one feed item to whoever subscribed to it, with what an outside actor wrote wrapped.
func renderGitHubItem(item contract.GitHubItem) string {
	said, wrapped := actionParts([]contract.GitHubItem{item}, nil)
	lines := []string{fmt.Sprintf("%s %s#%d: %s. %s", item.Kind, item.Repo, item.Number, strings.Join(said, "; "), item.URL)}
	lines = append(lines, wrapped...)
	if item.Text != "" {
		text := item.Text
		if item.Outside {
			text = contract.Outside("github", "@"+item.Actor, text)
		}
		lines = append(lines, text)
	}
	return strings.Join(lines, "\n")
}

// actionWords states an action in a few words. text is the part of them an outside actor wrote, which the caller wraps.
func actionWords(item contract.GitHubItem) (said, text string) {
	switch item.Action {
	case contract.GitHubLabeled:
		return "labels +" + item.Detail, "labels +" + item.Detail
	case contract.GitHubUnlabeled:
		return "labels -" + item.Detail, "labels -" + item.Detail
	case contract.GitHubMilestoned:
		return "milestone " + item.Detail, "milestone " + item.Detail
	case contract.GitHubRenamed:
		return "title " + item.Detail, "title " + item.Detail
	case contract.GitHubDemilestoned:
		return "milestone removed", ""
	case contract.GitHubAssigned:
		return "assigned @" + item.Detail, ""
	case contract.GitHubUnassigned:
		return "unassigned @" + item.Detail, ""
	case contract.GitHubReviewRequested:
		return "review requested from @" + item.Detail, ""
	case contract.GitHubReviewRequestRemoved:
		return "review request removed for @" + item.Detail, ""
	case contract.GitHubEdited:
		return "body edited", ""
	case contract.GitHubReopened:
		return "reopened", ""
	case contract.GitHubConvertedToDraft:
		return "now a draft", ""
	case contract.GitHubReadyForReview:
		return "ready for review", ""
	}
	return string(item.Action), ""
}

// actionParts states actions in order, a run by one actor as one part, so each reads with who did it. Text by anyone but the
// owner's account goes in wrapped instead, and the part only names what moved.
func actionParts(actions []contract.GitHubItem, words map[contract.PrChangeKind]string) (said, wrapped []string) {
	for start := 0; start < len(actions); {
		actor := actions[start].Actor
		var parts, text []string
		end := start
		for ; end < len(actions) && actions[end].Actor == actor; end++ {
			item := actions[end]
			word, free := actionWords(item)
			if told, ok := words[contract.PrChangeKind(item.Action)]; ok {
				word = told
			}
			if free != "" && item.Outside {
				word = strings.SplitN(free, " ", 2)[0] + " changed"
				text = append(text, free)
			}
			if !slices.Contains(parts, word) {
				parts = append(parts, word)
			}
		}
		said = append(said, strings.Join(parts, ", ")+by(actor))
		if len(text) > 0 {
			wrapped = append(wrapped, contract.Outside("github", "@"+actor, strings.Join(text, "\n")))
		}
		start = end
	}
	return said, wrapped
}

// changeHead states one item's changes in a line of facts, with any outside text wrapped beneath it. A merge or a close that
// has an action reads from that action, which knows who did it.
func changeHead(noun string, repo contract.RepoName, number int, url string, words map[contract.PrChangeKind]string, kinds []contract.PrChangeKind, actions []contract.GitHubItem) []string {
	said := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		acted := slices.ContainsFunc(actions, func(item contract.GitHubItem) bool { return string(item.Action) == string(kind) })
		if kind != contract.PrChangeMetadata && !acted {
			said = append(said, words[kind])
		}
	}
	parts := []string{}
	if len(said) > 0 {
		parts = append(parts, strings.Join(said, ", "))
	}
	acts, text := actionParts(actions, words)
	parts = append(parts, acts...)
	return append([]string{fmt.Sprintf("%s %s#%d: %s. %s", noun, repo, number, strings.Join(parts, "; "), url)}, text...)
}

// commentLines shows comments, wrapping those by anyone but the owner's account.
func commentLines(comments []contract.PrComment, more int) []string {
	var lines []string
	for _, comment := range comments {
		text := comment.Body
		if comment.Outside {
			text = contract.Outside("github", "@"+comment.Author, text)
		}
		lines = append(lines, fmt.Sprintf("Comment by @%s, %s:\n%s", comment.Author, comment.URL, text))
	}
	if more > 0 {
		lines = append(lines, fmt.Sprintf("%d more comments on GitHub.", more))
	}
	return lines
}

// renderPRChanged leaves out titles, which an outside author writes, unless one changed, and wraps what it must carry.
func renderPRChanged(b contract.PrChanged) string {
	var blocks []string
	for _, change := range b.Changes {
		if len(change.Kinds) == 0 {
			continue
		}
		lines := changeHead("PR", change.PR.Repo, change.PR.Number, change.PR.URL, prChangeWords, change.Kinds, change.Actions)
		if change.PR.Head != "" && slices.ContainsFunc(change.Kinds, func(kind contract.PrChangeKind) bool {
			return kind == contract.PrChangeCIFailed || kind == contract.PrChangeCIPassed
		}) {
			lines = append(lines, "The checks ran on "+change.PR.Head+".")
		}
		if len(change.FailedChecks) > 0 {
			checks := strings.Join(change.FailedChecks, ", ")
			if change.Outside {
				checks = contract.Outside("github", "@"+change.PR.Author, checks)
			}
			lines = append(lines, "Failed checks: "+checks)
		}
		if push := change.Push; push != nil {
			lines = append(lines, fmt.Sprintf("Head moved %s -> %s.", push.Before, push.After))
		}
		lines = append(lines, commentLines(change.Comments, change.MoreComments)...)
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return strings.Join(append(blocks, issueBlocks(b.Issues)...), "\n\n")
}

func issueBlocks(changes []contract.IssueChange) []string {
	var blocks []string
	for _, change := range changes {
		if len(change.Kinds) == 0 {
			continue
		}
		issue := change.Issue
		lines := changeHead("Issue", issue.Repo, issue.Number, issue.URL, issueChangeWords, change.Kinds, change.Actions)
		blocks = append(blocks, strings.Join(append(lines, commentLines(change.Comments, change.MoreComments)...), "\n"))
	}
	return blocks
}

// renderCheckin shows one line per session. Goal text is the sessions' own words, so it stays on one quoted line.
func renderCheckin(b contract.CheckinDue) string {
	lines := []string{"Check-in: these sessions hold open goals and nothing is moving them."}
	for i, child := range b.Children {
		if i == CheckinRows {
			lines = append(lines, fmt.Sprintf("%d more: session list", len(b.Children)-i))
			break
		}
		name := child.Name
		if child.Parent != "" {
			name += " (under " + child.Parent + ")"
		}
		line := fmt.Sprintf("- %s: %s for %s", name, child.State, roundMinutes(child.Idle))
		if child.Reason != "" {
			line += ", " + child.Reason
		}
		if child.Silent {
			line += ", last turn ended with no reply"
		}
		if child.Asking {
			line += ", has an open question for the owner"
		}
		line += fmt.Sprintf(". %s %q", child.Goal, clip(child.Text, 100))
		if child.Open > 0 {
			line += fmt.Sprintf(", %d open items", child.Open)
		}
		lines = append(lines, line)
	}
	lines = append(lines, "Quoted text is the sessions' own words. Progress on the goal clears the stall; a stall that stays goes up the tree.")
	return strings.Join(lines, "\n")
}

// renderFailures states what broke. A failed turn keeps its input, so the session needs a new start, not the same words.
func renderFailures(b contract.SessionFailed) string {
	lines := []string{"Sessions failed:"}
	for _, failure := range b.Failures {
		name := failure.Name
		if failure.Parent != "" {
			name += " (under " + failure.Parent + ", which cannot take this)"
		}
		what := "its turn failed; its input is kept, and its next input starts a new turn"
		switch failure.Box {
		case contract.BoxFailed:
			what = "its box failed; session restart discards unpushed work"
		case contract.BoxLost:
			what = "its box is lost and its turn is not running; the engine is trying to recover it"
		}
		line := "- " + name + ": " + what
		if failure.Cause != "" {
			line += " (" + string(failure.Cause) + ")"
		}
		if failure.Reason != "" {
			line += fmt.Sprintf(". %q", clip(failure.Reason, 200))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func roundMinutes(d contract.DurationMS) string {
	minutes := int64(d) / 60000
	if minutes < 120 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh", minutes/60)
}

// clip folds text to one line of at most n characters.
func clip(text string, n int) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > n {
		return string(runes[:n-1]) + "…"
	}
	return string(runes)
}

// Deliverable says whether a session can be told about an event of this type. An unknown type, or one
// that is never stored or never rendered, is not deliverable.
func Deliverable(kind contract.EventType) bool {
	body, err := contract.DecodeEventBody(kind, []byte("{}"))
	if err != nil {
		return false
	}
	_, err = RenderDelivery(body)
	return err == nil
}

func jobName(job contract.ShellJobID, description string) string {
	if description != "" {
		return "'" + description + "'"
	}
	return string(job)
}

// jobTime words a whole number of minutes.
func jobTime(minutes int) string {
	unit := func(n int, name string) string {
		if n == 1 {
			return "1 " + name
		}
		return fmt.Sprintf("%d %ss", n, name)
	}
	switch hours, rest := minutes/60, minutes%60; {
	case minutes < 1:
		return "under a minute"
	case hours == 0:
		return unit(rest, "minute")
	case rest == 0:
		return unit(hours, "hour")
	default:
		return unit(hours, "hour") + " " + unit(rest, "minute")
	}
}

func jobExit(code int) string {
	if code == 0 {
		return "has completed"
	}
	return fmt.Sprintf("failed with exit code %d", code)
}
