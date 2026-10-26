// Package stores owns the ordered migrations shared by engine startup and
// the in-process fake engine. Other production processes do not call it.
package stores

import (
	"fmt"

	"dyna-agent/internal/account/attempts"
	"dyna-agent/internal/account/reposcope"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/admission"
	"dyna-agent/internal/callerid"
	"dyna-agent/internal/callrequests"
	"dyna-agent/internal/cihealth"
	"dyna-agent/internal/computers"
	"dyna-agent/internal/devicegrant"
	"dyna-agent/internal/doctor"
	"dyna-agent/internal/doctoralert"
	"dyna-agent/internal/escalate"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/goals"
	harnesshealth "dyna-agent/internal/harness/health"
	"dyna-agent/internal/harness/modelcooldown"
	"dyna-agent/internal/harness/preferences"
	"dyna-agent/internal/harness/runtime/genstate"
	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/lanesync"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/library"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/maintenance"
	"dyna-agent/internal/notices"
	"dyna-agent/internal/parentcheckin"
	"dyna-agent/internal/plan/ancestry"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/state"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/transcript"
	"dyna-agent/internal/update"
	"dyna-agent/internal/webpush"
	"dyna-agent/internal/websvc/devicestore"
	"dyna-agent/internal/workflowtemplate"
	"dyna-agent/internal/worktrees"
)

// migrations brings every store in dyna.db to this version. Only the
// engine runs them, at start, so other processes find current tables
// (requirement 41). engine.TestEngineMigratesEveryStore keeps the list complete.
var migrations = []func(dataDir string) error{
	// First: a moved store's startup import reads its stamps through
	// state.ImportIfForeign.
	state.MigrateMirrors,
	reposcope.Migrate,
	accountstore.Migrate,
	devicegrant.Migrate,
	devicestore.Migrate,
	callerid.Migrate,
	callrequests.Migrate,
	computers.Migrate,
	admission.MigrateObserved,
	doctor.Migrate,
	doctoralert.Migrate,
	escalate.Migrate,
	goals.Migrate,
	harnesshealth.Migrate,
	// Model cooldowns are read by detached turn runners, which cannot migrate.
	modelcooldown.Migrate,
	preferences.Migrate,
	hooks.Migrate,
	hooks.MigrateRuns,
	hooks.MigrateDocuments,
	hookinstall.Migrate,
	cihealth.Migrate,
	// ghclient keeps the proved GitHub account of each room, so turn startup
	// makes no GitHub API call after an engine restart.
	ghclient.Migrate,
	// genstate keeps the generation leases and publication views the harness
	// runtime queries at cleanup.
	genstate.Migrate,
	lanesync.Migrate,
	librarianstate.Migrate,
	library.Migrate,
	localci.Migrate,
	// machines reads the old local_ci_nodes table, so it runs after localci.
	machines.Migrate,
	maintenance.Migrate,
	notices.Migrate,
	parentcheckin.Migrate,
	ancestry.Migrate,
	prstate.Migrate,
	watch.Migrate,
	questions.Migrate,
	room.Migrate,
	sessionlife.Migrate,
	shells.Migrate,
	state.MigrateRequests,
	subagent.Migrate,
	migrateQuestionWithdrawals,
	runstore.Migrate,
	transcript.MigrateSteeringAnchors,
	update.Migrate,
	attempts.Migrate,
	webpush.Migrate,
	worktrees.Migrate,
	workflowtemplate.Migrate,
}

// Migrate prepares every store in dataDir before the engine serves calls.
func Migrate(dataDir string) error {
	for _, migrate := range migrations {
		if err := migrate(dataDir); err != nil {
			return fmt.Errorf("migrate dyna.db: %w", err)
		}
	}
	return nil
}
