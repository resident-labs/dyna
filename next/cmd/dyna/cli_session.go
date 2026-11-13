package main

import (
	"encoding/json"
	"fmt"

	"github.com/resident-labs/dyna/contract"
	dynaroom "github.com/resident-labs/dyna/room"
	"github.com/spf13/cobra"
)

func sessionCommand(getClient func() client) *cobra.Command {
	session := &cobra.Command{Use: "session"}
	session.AddCommand(setAccountCommand(getClient))
	session.AddCommand(&cobra.Command{Use: "stop SESSION", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().SessionStop(cmd.Context(), contract.SessionStopRequest{Session: contract.SessionID(args[0])})
		if err != nil {
			return err
		}
		if !result.Stopping {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "No turn is running.")
		}
		return err
	}})
	var roomID, name, profile, goal, doneWhen, brief, sessionRepo, parent, size, composeName, sessionHost string
	var sameHost bool
	var gpu int
	start := &cobra.Command{Use: "start", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().SessionStart(cmd.Context(), contract.SessionStartRequest{Room: contract.RoomID(roomID), Name: name, Parent: contract.SessionID(parent), Repo: contract.RepoName(sessionRepo), Profile: contract.ProfileName(profile), Goal: goal, DoneWhen: doneWhen, Brief: brief, Size: contract.BoxSizeName(size), SameHost: sameHost, Host: contract.HostName(sessionHost), Compose: composeName, GPU: contract.GPUGiB(gpu)})
		if err == nil {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), r.Session)
		}
		return err
	}}
	start.Flags().StringVar(&roomID, "room", "", "Room ID")
	start.Flags().StringVar(&name, "name", "", "Session name")
	start.Flags().StringVar(&profile, "profile", "", "Profile")
	start.Flags().StringVar(&sessionRepo, "repo", "", "Session repository")
	start.Flags().StringVar(&parent, "parent", "", "Parent session ID")
	start.Flags().StringVar(&goal, "goal", "", "Goal")
	start.Flags().StringVar(&doneWhen, "done-when", "", "When the goal is done")
	start.Flags().StringVar(&brief, "brief", "", "Brief")
	start.Flags().StringVar(&size, "size", "", "Box size for a lead: agent, small, medium or large")
	start.Flags().BoolVar(&sameHost, "same-host", false, "Place the box on its parent’s host")
	start.Flags().StringVar(&sessionHost, "host", "", "Pin the new box to a named host; cannot combine with same-host")
	start.Flags().StringVar(&composeName, "compose", "", "Compose file, by name; without it the file named default, if any")
	start.Flags().IntVar(&gpu, "gpu", 0, "GiB of GPU memory the box asks for on a host card; 0 asks for none")
	closeCmd := &cobra.Command{Use: "close SESSION", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		closed, err := getClient().SessionClose(cmd.Context(), contract.SessionCloseRequest{Session: contract.SessionID(args[0])})
		if err == nil && len(closed.Handed) > 0 {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Handed to %s: %s\n", closed.To, dynaroom.ResponsibilityNames(closed.Handed))
		}
		if err == nil && len(closed.Folded) > 0 {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Folds into %s's goal as items, with their open items as subtasks: %s.\n", closed.To, dynaroom.GoalNames(closed.Folded))
		}
		if err == nil && len(closed.Unheld) > 0 {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Left with no holder, because %s is root and root holds no goals: %s. goal list --session %s shows them; goal done ends them, and settles their open items.\n", closed.To, dynaroom.GoalNames(closed.Unheld), closed.To)
		}
		return err
	}}
	restart := &cobra.Command{Use: "restart SESSION", Short: "Replace a failed session box", Long: "Replace a failed session box. The old volume and unpushed work are lost. Removes the container and workspace/stack volumes on every known current or prior host before resetting the box. If cleanup on any of those hosts fails, as when it does not answer, the box stays failed and the error names the host; run restart again once that host answers (host list shows it). Deletion may already be partial; it is not rolled back.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionRestart(cmd.Context(), contract.SessionRestartRequest{Session: contract.SessionID(args[0])})
		return err
	}}
	var refreshRoom string
	refresh := &cobra.Command{Use: "refresh [SESSION]", Short: "Mark a box to restart once nothing needs it", Long: "Mark a running box to restart once no turn, job or terminal needs it. It keeps its volume, unlike session restart, which discards it.", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if (len(args) == 1) == (refreshRoom != "") {
			return fmt.Errorf("give a session or --room")
		}
		request := contract.SessionRefreshRequest{Room: contract.RoomID(refreshRoom)}
		if len(args) == 1 {
			request.Session = contract.SessionID(args[0])
		}
		r, err := getClient().SessionRefresh(cmd.Context(), request)
		if err != nil {
			return err
		}
		if len(r.Marked) == 0 {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "No running box to mark.")
		}
		for _, id := range r.Marked {
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), id); err != nil {
				return err
			}
		}
		return err
	}}
	refresh.Flags().StringVar(&refreshRoom, "room", "", "Mark every open session in this room")
	setProfile := &cobra.Command{Use: "set-profile SESSION PROFILE", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionSetProfile(cmd.Context(), contract.SessionSetProfileRequest{Session: contract.SessionID(args[0]), Profile: contract.ProfileName(args[1])})
		return err
	}}
	setBranch := &cobra.Command{Use: "set-branch SESSION BRANCH", Short: "Make BRANCH the session's branch on its repository (owner socket only; renames nothing on GitHub)", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionSetBranch(cmd.Context(), contract.SessionSetBranchRequest{Session: contract.SessionID(args[0]), Branch: args[1]})
		return err
	}}
	session.AddCommand(&cobra.Command{Use: "list ROOM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().SessionList(cmd.Context(), contract.SessionListRequest{Room: contract.RoomID(args[0])})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Sessions)
	}}, &cobra.Command{Use: "responsibilities SESSION", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := getClient().SessionResponsibilities(cmd.Context(), contract.SessionResponsibilitiesRequest{Session: contract.SessionID(args[0])})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}}, &cobra.Command{Use: "move SESSION NEW_PARENT", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := getClient().SessionMove(cmd.Context(), contract.SessionMoveRequest{Session: contract.SessionID(args[0]), NewParent: contract.SessionID(args[1])})
		return err
	}})
	var composeRoom, composeRepo string
	composeList := &cobra.Command{Use: "compose-list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := getClient().ComposeList(cmd.Context(), contract.ComposeListRequest{Room: contract.RoomID(composeRoom), Repo: contract.RepoName(composeRepo)})
		if err != nil {
			return err
		}
		for _, file := range r.Files {
			line := file.Name + " (" + string(file.Level) + ")"
			if file.Problem != "" {
				line += ": " + file.Problem
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), line); err != nil {
				return err
			}
		}
		return nil
	}}
	composeList.Flags().StringVar(&composeRoom, "room", "", "Room ID")
	composeList.Flags().StringVar(&composeRepo, "repo", "", "Repository a child would use")
	session.AddCommand(start, closeCmd, restart, refresh, setProfile, setBranch, composeList)
	return session
}
