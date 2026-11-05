package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestWorkflowSetReadsTypedValues(t *testing.T) {
	got, err := parseWorkflowSettings([]string{"start=leads", "maxRuns=3", "maxWorkers=0", "timeout=90", "size=large", "profiles=fast,deep", "worktree=off", "merge=on", "run=off", "workflow=off", "post=off", "resume=on"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Start != contract.WorkflowStartLeads || *got.MaxRuns != 3 || *got.MaxWorkers != 0 || *got.Timeout != 90 || got.Size != contract.BoxSizeLarge || len(got.Profiles) != 2 || got.Profiles[1] != "deep" {
		t.Fatalf("values %+v", got)
	}
	if *got.Worktree || !*got.Merge || *got.Run || *got.Workflow || *got.Post || !*got.Resume {
		t.Fatalf("switches %+v", got)
	}
	if len(got.Named()) != len(contract.AllWorkflowSettings) {
		t.Fatalf("named %v of %d settings", got.Named(), len(contract.AllWorkflowSettings))
	}
	for _, bad := range []string{"colour=red", "start", "maxRuns=many", "post=maybe", "profiles=", "profiles=fast,"} {
		if _, err := parseWorkflowSettings([]string{bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestWorkflowSetHelpNamesEverySettingWithItsLine(t *testing.T) {
	var out bytes.Buffer
	command := workflowCommand(func() *socketClient { return nil })
	command.SetOut(&out)
	command.SetArgs([]string{"set", "--help"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, info := range contract.AllWorkflowSettings {
		if !strings.Contains(out.String(), string(info.Setting)+"  "+info.Help) {
			t.Errorf("help lacks %s: %s", info.Setting, info.Help)
		}
	}
}

// --github-writes is read strictly: one @, no empty element, known groups, owner/name or * for repositories.
func TestParseGitHubWrites(t *testing.T) {
	got, err := parseGitHubWrites("issues,projects@acme/app,*")
	if err != nil || len(got.Groups) != 2 || got.Groups[1] != contract.GitHubWriteProjects || len(got.Repos) != 2 || got.Repos[0] != "acme/app" || got.Repos[1] != "*" {
		t.Fatalf("a value: %+v %v", got, err)
	}
	off, err := parseGitHubWrites("off")
	if err != nil || off.Groups == nil || len(off.Groups) != 0 || off.Repos == nil || len(off.Repos) != 0 {
		t.Fatalf("off: %+v %v", off, err)
	}
	for _, bad := range []string{"@", "a@", "@b", "issues@", "@acme/app", "issues,@acme/app", ",issues@acme/app", "issues@acme/app,", "issues@acme/app,,acme/lib", "issues@acme/app@acme/lib", "issues@@acme/app",
		"issues", "issues acme/app", "issues:acme/app", "merge@acme/app", "Issues@acme/app", "issues,,pulls@acme/app", "issues@acme", "issues@acme/", "issues@/app", "issues@acme/app/x", "issues@**", "OFF", "off@acme/app", "issues@acme/app, acme/lib"} {
		if got, err := parseGitHubWrites(bad); err == nil {
			t.Errorf("%q was accepted as %+v", bad, got)
		}
	}
}
