// Package job provides `dyna job`: one command, run detached on a node.
package job

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/jobs"
	"dyna-agent/internal/machines"
)

// jobView is what every job command prints: the facts of the stored row.
type jobView struct {
	ID         string   `json:"id"`
	Node       string   `json:"node"`
	State      string   `json:"state"`
	Command    []string `json:"command"`
	Repo       string   `json:"repo,omitempty"`
	SHA        string   `json:"sha,omitempty"`
	ExitCode   *int     `json:"exitCode,omitempty"`
	Note       string   `json:"note,omitempty"`
	WaitReason string   `json:"waitReason,omitempty"`
	Idle       string   `json:"idle,omitempty"`
	Log        string   `json:"log,omitempty"`
	// Artifacts names the kept outputs and how to fetch them.
	Artifacts []string                `json:"artifacts,omitempty"`
	Fetch     string                  `json:"fetch,omitempty"`
	Workspace string                  `json:"workspace,omitempty"`
	Resources []machines.ResourceName `json:"resources,omitempty"`
}

func view(job jobs.Job) jobView {
	v := jobView{ID: job.ID, Node: job.Node, State: string(job.State), Command: job.Command, Repo: job.Repo, SHA: job.SHA, ExitCode: job.ExitCode, Note: job.Note, WaitReason: job.WaitReason, Log: "dyna job logs " + job.ID, Workspace: job.Workspace, Resources: job.Resources}
	if job.Idle > 0 {
		v.Idle = job.Idle.String()
	}
	if len(job.Artifacts) > 0 {
		v.Artifacts, v.Fetch = job.Artifacts, "dyna job fetch "+job.ID
	}
	return v
}

func printJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "job",
		Short: "Run one command on a node, detached",
		Args:  cobra.NoArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return c.Help() },
		Long: "A job is one command that runs on a node, detached: it survives the\n" +
			"end of this command, an SSH drop and an engine restart. With no --node or --label it\n" +
			"runs on this node, so one node needs no setup. --label chooses the eligible node with the most free queue capacity. The node's\n" +
			"queue admits the job when its CPUs, memory, IO weight and named resources fit; a job that\n" +
			"waits shows why in dyna job show. Its log and exit code are stored; the\n" +
			"session that submitted it is woken when the job ends. --artifact keeps\n" +
			"named outputs of its work folder after it ends; dyna job fetch copies\n" +
			"them here.",
	}
	cmd.AddCommand(runCmd(), showCmd(), logsCmd(), listCmd(), cancelCmd(), fetchCmd())
	return cmd
}

var callRunJob = func(ctx context.Context, request callspec.RunJobInput, job *jobs.Job) error {
	return engineclient.New("").Call(ctx, "RunJob", []any{request}, job)
}

func runCmd() *cobra.Command {
	var node, commit, image, memory, ioWeight, network string
	var artifacts, resourceNames, labels, mounts, devices []string
	var cpus int
	var host, workspace bool
	var timeout, idle time.Duration
	c := &cobra.Command{
		Use:   "run [--node N | --label L] [--commit REF] [--cpus N] [--memory SIZE] [--resource NAME] [--workspace] [--timeout D] [--idle D] [--image IMG | --host] [--network none] [--mount NAME:PATH] [--device PATH] -- CMD",
		Short: "Submit a job and return",
		Args: func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("a job needs a command after --")
			}
			return nil
		},
		RunE: func(c *cobra.Command, args []string) error {
			if workspace && commit == "" {
				return fmt.Errorf("--workspace needs --commit")
			}
			if host && image != "" {
				return fmt.Errorf("a job runs either on the host or in an image, not both")
			}
			if ioWeight != "" && ioWeight != "heavy" && ioWeight != "light" {
				return fmt.Errorf("io must be heavy or light, not %q", ioWeight)
			}
			repo := ""
			if commit != "" {
				repo, _ = os.Getwd()
			}
			request := callspec.RunJobInput{
				Node: node, Labels: labels, Command: args, Repo: repo, Ref: commit,
				Host: host, Image: image, Network: network, Mounts: mounts, Devices: devices,
				CPUs: cpus, Memory: memory,
				Timeout: int(timeout / time.Second), IO: ioWeight, Artifacts: artifacts,
				Resources: resourceNames, Workspace: workspace,
			}
			if c.Flags().Changed("idle") {
				seconds := int(idle / time.Second)
				request.Idle = &seconds
			}
			var job jobs.Job
			err := callRunJob(c.Context(), request, &job)
			if err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), view(job))
		},
	}
	c.Flags().StringVar(&node, "node", "", "node to run on (empty: this node, or the roomiest node with --label)")
	c.Flags().StringArrayVar(&labels, "label", nil, "run on the roomiest node with every label, such as arch=x86_64 (repeatable)")
	c.Flags().StringVar(&commit, "commit", "", "push this commit of the current repository and check it out (empty: no source)")
	c.Flags().IntVar(&cpus, "cpus", 0, "CPUs the job may use")
	c.Flags().StringVar(&memory, "memory", "", "memory the job may use, such as 16g or 512m")
	c.Flags().DurationVar(&timeout, "timeout", 0, "deadline of the job (default 30m)")
	c.Flags().DurationVar(&idle, "idle", 0, "stop a running job after this long without output (default: none)")
	c.Flags().StringVar(&image, "image", "", "run the command in this container image")
	c.Flags().StringVar(&network, "network", "", "container network; only none is supported")
	c.Flags().StringArrayVar(&mounts, "mount", nil, "mount NAME:CONTAINER_PATH[:ro|rw] from this room's node registrations (repeatable)")
	c.Flags().StringArrayVar(&devices, "device", nil, "allow this container to use a device on this room's node allow list (repeatable)")
	c.Flags().BoolVar(&host, "host", false, "run the command on the node itself (the default without --image)")
	c.Flags().StringArrayVar(&artifacts, "artifact", nil, "keep this path of the job's work folder after it ends, such as target/release/app (repeatable)")
	c.Flags().StringVar(&ioWeight, "io", "light", "the job's IO weight: a node runs only a few heavy jobs at once")
	c.Flags().StringArrayVar(&resourceNames, "resource", nil, "hold one unit of this named resource for the job's whole run (repeatable)")
	c.Flags().BoolVar(&workspace, "workspace", false, "reuse this session's kept checkout on the node (needs --commit)")
	return c
}

func fetchCmd() *cobra.Command {
	var to string
	c := &cobra.Command{
		Use:   "fetch <id> [--to DIR]",
		Short: "Copy a finished job's kept artifacts to a folder",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var job jobs.Job
			if err := engineclient.New("").Call(c.Context(), "JobShow", []any{args[0]}, &job); err != nil {
				return err
			}
			if !job.State.Finished() {
				return fmt.Errorf("job %s is still %s; fetch it after it ends", job.ID, job.State)
			}
			dir := jobs.ArtifactDir(job.LogPath)
			if _, err := os.Stat(dir); err != nil {
				return fmt.Errorf("job %s kept no artifacts (run it with --artifact PATH)", job.ID)
			}
			if err := os.MkdirAll(to, 0o755); err != nil {
				return err
			}
			// cp keeps file modes, so a built binary stays executable, and a
			// second fetch into the same folder replaces the first.
			if out, err := exec.CommandContext(c.Context(), "cp", "-pR", dir+"/.", to).CombinedOutput(); err != nil {
				return fmt.Errorf("copy the artifacts of job %s: %w: %s", job.ID, err, out)
			}
			_, err := fmt.Fprintf(c.OutOrStdout(), "copied the artifacts of job %s to %s\n", job.ID, to)
			return err
		},
	}
	c.Flags().StringVar(&to, "to", ".", "folder to copy the artifacts into")
	return c
}

func showCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show one job",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var job jobs.Job
			if err := engineclient.New("").Call(c.Context(), "JobShow", []any{args[0]}, &job); err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), view(job))
		},
	}
}

func logsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs <id>",
		Short: "Print the last of a job's output",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var out struct {
				ID  string `json:"id"`
				Log string `json:"log"`
			}
			if err := engineclient.New("").Call(c.Context(), "JobLogs", []any{args[0]}, &out); err != nil {
				return err
			}
			if out.Log != "" {
				fmt.Fprintln(c.OutOrStdout(), out.Log)
			}
			return nil
		},
	}
}

func listCmd() *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List jobs (--all includes finished ones)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var list []jobs.Job
			if err := engineclient.New("").Call(c.Context(), "JobList", []any{!all}, &list); err != nil {
				return err
			}
			for _, job := range list {
				fmt.Fprintf(c.OutOrStdout(), "%s  %-12s %-9s  %v\n", job.ID, job.Node, job.State, job.Command)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "include finished jobs")
	return c
}

func cancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <id>",
		Short: "Stop a queued or running job",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var job jobs.Job
			if err := engineclient.New("").Call(c.Context(), "CancelJob", []any{args[0]}, &job); err != nil {
				return err
			}
			return printJSON(c.OutOrStdout(), view(job))
		},
	}
}
