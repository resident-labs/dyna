// Package testfixture shares real PR stores and fake GH processes in integration tests.
package testfixture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
)

// FakeGH installs a fake gh process that logs arguments and returns fixture JSON.
func FakeGH(t *testing.T) string {
	return FakeGHWithPull(t, watch.Pull{Number: 7, Title: "Add knobs", State: "closed", Merged: true})
}

func FakeGHWithPull(t *testing.T, pull watch.Pull) string {
	t.Helper()
	t.Setenv(ghclient.BinaryEnv, "")
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	pullPath := filepath.Join(dir, "pull.json")
	pullJSON, err := json.Marshal(pull)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pullPath, pullJSON, 0600); err != nil {
		t.Fatal(err)
	}
	commentPages := make([]string, 2)
	for page, count := range []int{100, 49} {
		comments := make([]map[string]any, count)
		for i := range comments {
			comments[i] = map[string]any{"body": fmt.Sprintf("comment-%d", page*100+i), "author": map[string]string{"login": "sam"}}
		}
		issue := map[string]any{"number": 3, "title": "Long discussion", "state": "OPEN", "url": "https://github.com/acme/widgets/issues/3", "labels": map[string]any{"nodes": []any{}}, "closedByPullRequestsReferences": map[string]any{"nodes": []any{}}, "comments": map[string]any{"nodes": comments, "pageInfo": map[string]any{"hasNextPage": page == 0, "endCursor": fmt.Sprint(page)}}}
		raw, err := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"issue": issue}}})
		if err != nil {
			t.Fatal(err)
		}
		commentPages[page] = filepath.Join(dir, fmt.Sprintf("comments-%d.json", page))
		if err := os.WriteFile(commentPages[page], append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
if [ -n "$GIT_DIR" ]; then echo 'inherited GIT_DIR' >&2; exit 1; fi
echo "$PWD|$*" >> "` + log + `"
case "$1 $2" in
"api graphql")
 case "$*" in
 *"query IssueList"*) echo '{"data":{"repository":{"nameWithOwner":"acme/widgets","url":"https://github.com/acme/widgets"},"search":{"pageInfo":{"hasNextPage":false},"nodes":[{"number":3,"title":"Knobs squeak","state":"CLOSED","stateReason":"COMPLETED","author":{"login":"bot","__typename":"Bot"},"labels":{"nodes":[]},"closedByPullRequestsReferences":{"nodes":[{"number":7,"state":"OPEN","isDraft":true,"url":"https://github.com/acme/widgets/pull/7"}]},"createdAt":"2026-09-01T10:00:00Z","updatedAt":"2026-09-02T10:00:00Z","url":"https://github.com/acme/widgets/issues/3"}]}}}' ;;
 *"number=4"*) echo '{"data":{"repository":{"issue":{"number":4,"title":"Linked issue","state":"OPEN","url":"https://github.com/acme/widgets/issues/4","body":"Issue body","labels":{"nodes":[]},"comments":{"nodes":[{"author":{"login":"bot","__typename":"Bot"},"body":"Comment","createdAt":"2026-10-02T11:00:00Z","url":"comment-url"}]},"closedByPullRequestsReferences":{"nodes":[{"number":8,"state":"OPEN","isDraft":false,"url":"https://github.com/acme/widgets/pull/8"}]}}}}}' ;;
 *"number=3"*)
 if [ "$GH_TEST_LONG_COMMENTS" = true ]; then
  cat "` + commentPages[0] + `"
  case "$*" in *--paginate*) cat "` + commentPages[1] + `" ;; esac
  exit 0
 fi
 echo '{"data":{"repository":{"issue":{"number":3,"title":"Knobs squeak","state":"OPEN","url":"https://github.com/acme/widgets/issues/3","body":"RESPOND: repair knobs","labels":{"nodes":[]},"comments":{"nodes":[]},"closedByPullRequestsReferences":{"nodes":[]}}}}}' ;;
 *) echo 'GraphQL: Could not resolve to an issue' >&2; exit 1 ;;
 esac ;;

"repo view") printf '{"nameWithOwner":"%s/%s","url":"https://%s/%s/%s"}\n' "${GH_TEST_OWNER:-acme}" "${GH_TEST_REPO:-widgets}" "${GH_TEST_HOST:-github.com}" "${GH_TEST_OWNER:-acme}" "${GH_TEST_REPO:-widgets}" ;;
"pr list") echo '[{"number":7,"title":"Add knobs","state":"OPEN","isDraft":true,"author":{"login":"sam","is_bot":false},"labels":[{"name":"ui","color":"00ff00"}],"createdAt":"2026-10-01T10:00:00Z","updatedAt":"2026-10-02T10:00:00Z","url":"https://github.com/acme/widgets/pull/7","headRefName":"feat/knobs","baseRefName":"main","reviewDecision":"REVIEW_REQUIRED","additions":12,"deletions":3}]' ;;
"pr view") echo '{"number":7,"title":"Add knobs","state":"OPEN","author":{"login":"sam"},"body":"Adds **knobs**.","comments":[{"author":{"login":"rev"},"body":"Nice","createdAt":"2026-10-02T11:00:00Z","url":"u"}],"reviews":[{"author":{"login":"rev"},"body":"","state":"APPROVED","submittedAt":"2026-10-02T12:00:00Z"}],"statusCheckRollup":[{"__typename":"CheckRun","name":"go-test","workflowName":"CI","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"d"},{"__typename":"CheckRun","name":"lint","status":"IN_PROGRESS","conclusion":""},{"__typename":"StatusContext","context":"CodeRabbit","state":"SUCCESS","targetUrl":""}],"files":[{"path":"knob.go","additions":12,"deletions":3,"changeType":"MODIFIED"}],"changedFiles":1,"commits":[{"oid":"1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","messageHeadline":"Add knobs","authoredDate":"2026-10-01T10:00:00Z","authors":[{"login":"sam","name":"Sam"}]},{"oid":"2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","messageHeadline":"Fix knob range","authoredDate":"2026-10-02T09:00:00Z","authors":[]}],"mergeable":"MERGEABLE","labels":[],"createdAt":"2026-10-01T10:00:00Z","updatedAt":"2026-10-02T10:00:00Z","url":"https://github.com/acme/widgets/pull/7"}' ;;
"pr diff") printf 'diff --git a/knob.go b/knob.go\n+knob\n' ;;
"api --hostname")
 printf 'HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'
 case "$4" in
 */reviews*|*/comments*) echo '[]' ;;
 */check-runs*) echo '{"check_runs":[]}' ;;
 */actions/runs*) echo '{"workflow_runs":[]}' ;;
 */status*) echo '{"statuses":[]}' ;;
 */actions/workflows*) echo '{"workflows":[]}' ;;
 *) cat "` + pullPath + `" ;;
 esac ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func SaveState(t *testing.T, root string, st watch.State) {
	t.Helper()
	if st.Version == 0 {
		st.Version = 1
	}
	if err := state.Write(root, st); err != nil {
		t.Fatal(err)
	}
}
