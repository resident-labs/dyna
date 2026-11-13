package main

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
)

func workflowCommand(get func() *socketClient) *cobra.Command {
	c := &cobra.Command{Use: "workflow"}
	var room, args, resume, clientID string
	run := &cobra.Command{Use: "run FILE", Short: "Start a workflow script. Its result arrives as a final reply.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, files []string) error {
		script, err := os.ReadFile(files[0])
		if err != nil {
			return err
		}
		return printCall[contract.WorkflowRunResponse](cmd, get(), contract.MethodWorkflowRun, contract.WorkflowRunRequest{Room: contract.RoomID(room), Script: string(script), Args: args, Resume: contract.WorkflowRunID(resume), ClientID: clientID})
	}}
	run.Flags().StringVar(&room, "room", "", "Room; a session may omit it for its own")
	run.Flags().StringVar(&args, "args", "", "JSON the script reads as args")
	run.Flags().StringVar(&resume, "resume", "", "An ended run whose finished steps this run keeps, so only changed steps run; reload changes a running one")
	run.Flags().StringVar(&clientID, "client-id", "", "Retry key: the same key returns the first run")
	reload := &cobra.Command{Use: "reload RUN FILE", Short: "Give a running run a new script, in the same crew and box. Finished steps return their results, running ones are joined, and new ones start.", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		script, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		return printCall[contract.WorkflowReloadResponse](cmd, get(), contract.MethodWorkflowReload, contract.WorkflowReloadRequest{Run: contract.WorkflowRunID(args[0]), Script: string(script)})
	}}
	status := &cobra.Command{Use: "status RUN", Short: "Show one run and its steps", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, runs []string) error {
		return printCall[contract.WorkflowStatusResponse](cmd, get(), contract.MethodWorkflowStatus, contract.WorkflowStatusRequest{Run: contract.WorkflowRunID(runs[0])})
	}}
	list := &cobra.Command{Use: "list", Short: "List a room's workflow runs, newest first", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return printCall[contract.WorkflowListResponse](cmd, get(), contract.MethodWorkflowList, contract.WorkflowListRequest{Room: contract.RoomID(room)})
	}}
	list.Flags().StringVar(&room, "room", "", "Room; a session may omit it for its own")
	var clear []string
	var settingHelp strings.Builder
	for _, info := range contract.AllWorkflowSettings {
		fmt.Fprintf(&settingHelp, "  %s  %s\n", info.Setting, info.Help)
	}
	settings := &cobra.Command{Use: "settings", Short: "Show the workflow settings: defaults, global values, and each room's own values and what applies there (owner only)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return printCall[contract.WorkflowSettingsResponse](cmd, get(), contract.MethodWorkflowSettings, contract.WorkflowSettingsRequest{Room: contract.RoomID(room)})
	}}
	settings.Flags().StringVar(&room, "room", "", "Show only this room")
	set := &cobra.Command{Use: "set [SETTING=VALUE...]", Short: "Change workflow settings, in one room or for all rooms (owner only)", Long: "Change workflow settings for one room with --room, or for every room that does not set its own without it. A room's value wins over the global one. A bool setting takes on or off; profiles takes a comma-separated list. --clear returns a setting to the value it inherits.\n\nSettings:\n" + settingHelp.String(), Example: "  dyna workflow set start=leads maxRuns=3\n  dyna workflow set --room ROOM worktree=off profiles=fast,deep\n  dyna workflow set --room ROOM --clear start", RunE: func(cmd *cobra.Command, pairs []string) error {
		change, err := parseWorkflowSettings(pairs)
		if err != nil {
			return err
		}
		clearing := make([]contract.WorkflowSetting, 0, len(clear))
		for _, name := range clear {
			clearing = append(clearing, contract.WorkflowSetting(name))
		}
		_, err = call[contract.WorkflowSetRequest, contract.WorkflowSetResponse](cmd.Context(), get(), contract.MethodWorkflowSet, contract.WorkflowSetRequest{Room: contract.RoomID(room), Set: change, Clear: clearing})
		return err
	}}
	set.Flags().StringVar(&room, "room", "", "Room to change; without it, the global values")
	set.Flags().StringSliceVar(&clear, "clear", nil, "Settings to return to what they inherit")
	steer := &cobra.Command{Use: "steer RUN STEP TEXT", Args: cobra.ExactArgs(3), RunE: func(cmd *cobra.Command, args []string) error {
		return printCall[contract.WorkflowSteerResponse](cmd, get(), contract.MethodWorkflowSteer, contract.WorkflowSteerRequest{Run: contract.WorkflowRunID(args[0]), Step: args[1], Text: args[2]})
	}}
	pause := &cobra.Command{Use: "pause RUN", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return printCall[contract.WorkflowPauseResponse](cmd, get(), contract.MethodWorkflowPause, contract.WorkflowPauseRequest{Run: contract.WorkflowRunID(args[0])})
	}}
	unpause := &cobra.Command{Use: "unpause RUN", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return printCall[contract.WorkflowUnpauseResponse](cmd, get(), contract.MethodWorkflowUnpause, contract.WorkflowUnpauseRequest{Run: contract.WorkflowRunID(args[0])})
	}}
	c.AddCommand(steer, pause, unpause, run, reload, status, list, settings, set)
	return c
}

// parseWorkflowSettings reads SETTING=VALUE arguments into typed settings, so the engine gets values and no text.
func parseWorkflowSettings(pairs []string) (contract.WorkflowSettings, error) {
	var set contract.WorkflowSettings
	for _, pair := range pairs {
		name, value, ok := strings.Cut(pair, "=")
		if !ok {
			return set, fmt.Errorf("%q is not SETTING=VALUE", pair)
		}
		var number int
		var on bool
		var err error
		switch contract.WorkflowSetting(name) {
		case contract.WorkflowSettingStart:
			set.Start = contract.WorkflowStarters(value)
		case contract.WorkflowSettingProfiles:
			for _, profile := range strings.Split(value, ",") {
				if profile == "" {
					return set, fmt.Errorf("profiles takes names separated by commas; --clear profiles allows every profile")
				}
				set.Profiles = append(set.Profiles, contract.ProfileName(profile))
			}
		case contract.WorkflowSettingSize:
			set.Size = contract.BoxSizeName(value)
		case contract.WorkflowSettingMaxRuns, contract.WorkflowSettingMaxWorkers, contract.WorkflowSettingTimeout:
			if number, err = strconv.Atoi(value); err != nil {
				return set, fmt.Errorf("%s takes a whole number: %w", name, err)
			}
			switch contract.WorkflowSetting(name) {
			case contract.WorkflowSettingMaxRuns:
				set.MaxRuns = &number
			case contract.WorkflowSettingMaxWorkers:
				set.MaxWorkers = &number
			default:
				set.Timeout = &number
			}
		default:
			if !slices.Contains(contract.AllWorkflowFeatures, contract.WorkflowFeature(name)) {
				return set, fmt.Errorf("no workflow setting is named %q; see dyna workflow set --help", name)
			}
			if value != "on" && value != "off" {
				return set, fmt.Errorf("%s is on or off", name)
			}
			on = value == "on"
			switch contract.WorkflowFeature(name) {
			case contract.WorkflowFeatureWorktree:
				set.Worktree = &on
			case contract.WorkflowFeatureMerge:
				set.Merge = &on
			case contract.WorkflowFeatureRun:
				set.Run = &on
			case contract.WorkflowFeatureWorkflow:
				set.Workflow = &on
			case contract.WorkflowFeaturePost:
				set.Post = &on
			case contract.WorkflowFeatureResume:
				set.Resume = &on
			}
		}
	}
	return set, nil
}
