package runtime

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"dyna-agent/internal/engineclient"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/profile"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// engineClient keeps engine failure tests on a private socket without restart waits.
var engineClient = func() *engineclient.Client { return engineclient.New("") }

func output(c *cobra.Command, v any) error {
	e := json.NewEncoder(c.OutOrStdout())
	e.SetIndent("", "  ")
	return e.Encode(v)
}
func interactive(c *cobra.Command) bool {
	in, ok := c.InOrStdin().(*os.File)
	return ok && term.IsTerminal(int(in.Fd()))
}

// choosePersonal asks whether to copy the personal login that Dyna found.
func choosePersonal(in io.Reader, out io.Writer, found *harnessruntime.PersonalLogin) bool {
	if found.Saved {
		fmt.Fprintf(out, "Your personal account %s is already saved in Dyna. Sign in with another account.\n", found.Label)
		return false
	}
	fmt.Fprintf(out, "Dyna found your personal %s account %s.\n", found.Harness, found.Label)
	fmt.Fprint(out, "Use it (Dyna copies the login once) or sign in with another account? [U/s] ")
	answer, _ := bufio.NewReader(in).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "" || answer == "u" || answer == "use"
}

// newFirstUse installs bundles and reports each download on stderr.
func newFirstUse(c *cobra.Command) *harnessruntime.FirstUse {
	f := harnessruntime.NewFirstUse()
	f.Progress = func(harness, version string) {
		fmt.Fprintf(c.ErrOrStderr(), "Installing %s %s. This can take a minute.\n", harness, version)
	}
	return f
}

func NewCommand() *cobra.Command {
	root := &cobra.Command{Use: "harness", Short: "Manage Dyna's private harness binaries and native accounts"}
	var version string
	install := &cobra.Command{Use: "install <harness>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		if err := harnessruntime.New().EnsureCatalog(); err != nil {
			return err
		}
		firstUse := newFirstUse(c)
		v := version
		if v == "" {
			var err error
			if v, err = firstUse.LatestVersion(c.Context(), a[0]); err != nil {
				return err
			}
		}
		firstUse.Progress(a[0], v)
		b, err := firstUse.Bundles.InstallAndSelect(c.Context(), a[0], v)
		if err != nil {
			return err
		}
		return output(c, b)
	}}
	install.Flags().StringVar(&version, "version", "", "exact package version (default: the latest verified version)")
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		b, err := harnessruntime.NewBundleManager().List()
		if err != nil {
			return err
		}
		return output(c, b)
	}}
	var name string
	doctor := &cobra.Command{Use: "doctor", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		s, err := profile.Load(profile.DefaultPath())
		if err != nil {
			return err
		}
		selected := []profile.Profile{}
		for _, p := range s.Profiles {
			if name == "" || p.Name == name {
				selected = append(selected, p)
			}
		}
		rows := harnessruntime.New().Doctors(c.Context(), selected)
		return output(c, struct {
			Profiles     []harnessruntime.Doctor     `json:"profiles"`
			Capabilities []harnessruntime.Capability `json:"capabilities"`
		}{rows, harnessruntime.Capabilities()})
	}}
	doctor.Flags().StringVar(&name, "profile", "", "one profile name")
	gc := &cobra.Command{Use: "gc", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		// The engine removes the runtime's unused parts, so an account record it
		// drops publishes the account change from the engine process.
		return engineClient().Call(c.Context(), "GCPrivateRuntime", nil, nil)
	}}
	accounts := &cobra.Command{Use: "accounts", Short: "Manage private native login homes"}
	accounts.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		accounts := harnessruntime.NewAccounts()
		if err := accounts.MigrateLegacyGrants(); err != nil {
			return err
		}
		a, err := accounts.List()
		if err != nil {
			return err
		}
		type row struct {
			harnessruntime.NativeAccount
			RunningTurns int `json:"runningTurns"`
		}
		rows := make([]row, len(a))
		for i, n := range a {
			_, running := accounts.SwitchStatus(n.Harness)
			rows[i] = row{n, running}
		}
		return output(c, rows)
	}})
	var label, accountID string
	var usePersonal, signIn, acpLogin bool
	login := &cobra.Command{Use: "login <harness>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		harness, err := harnessruntime.CanonicalHarness(a[0])
		if err != nil {
			return err
		}
		if acpLogin && (harness != "antigravity" || accountID == "") {
			return errors.New("--acp signs in to Antigravity's ACP server and needs --account <account-id>")
		}
		id := accountID
		if id == "" && !signIn {
			// The engine installs the bundle while it looks for the login, so
			// this call comes first even when the copy is already chosen.
			var found *harnessruntime.PersonalLogin
			if err := engineClient().Call(c.Context(), "FindPersonalLogin", []any{harness, ""}, &found); err != nil {
				return err
			}
			use := usePersonal || (interactive(c) && found != nil && choosePersonal(c.InOrStdin(), c.ErrOrStderr(), found))
			if use {
				// The engine copies the login; the CLI reads the account it made.
				var snapshot harnessruntime.PrivateRuntimeSnapshot
				if err := engineClient().Call(c.Context(), "UsePersonalLogin", []any{harness}, &snapshot); err != nil {
					return err
				}
				for _, account := range snapshot.Accounts {
					if account.Harness == harness && account.Active {
						return output(c, account)
					}
				}
				return errors.New("the personal login was copied, but no active account was reported")
			}
		}
		accounts := harnessruntime.NewAccounts()
		b, _, err := newFirstUse(c).EnsureBundle(c.Context(), a[0], "")
		if err != nil {
			return err
		}
		if id == "" {
			account, err := accounts.Create(a[0], label)
			if err != nil {
				return err
			}
			id = account.ID
		}
		account, err := accounts.Find(id)
		if err != nil {
			return err
		}
		if account.Harness != b.Harness {
			return errors.New("private account belongs to another harness")
		}
		if acpLogin && !account.Active {
			// The ACP token is stored beside the live CLI token, so it must belong to this account.
			return fmt.Errorf("activate the account first: dyna harness accounts activate %s", id)
		}
		flow, err := accounts.BeginLogin(c.Context(), id, b.Executable)
		if err != nil {
			return err
		}
		completed := false
		defer func() {
			if !completed {
				_ = flow.Cancel()
			}
		}()
		if flow.Warning != "" {
			fmt.Fprintln(c.ErrOrStderr(), flow.Warning)
		}
		if acpLogin {
			if err := antigravityACPLogin(c, b.Executable, account.Identity, flow); err != nil {
				return err
			}
		} else {
			cmd := exec.CommandContext(c.Context(), flow.Argv[0], flow.Argv[1:]...)
			cmd.Env = append(os.Environ(), flow.Env...)
			cmd.Dir = flow.Cwd
			cmd.Stdin = os.Stdin
			cmd.Stdout = c.OutOrStdout()
			cmd.Stderr = c.ErrOrStderr()
			if err := cmd.Run(); err != nil {
				return errors.New("native sign-in did not complete")
			}
		}
		result, err := flow.Complete(c.Context())
		if err != nil {
			return err
		}
		completed = true

		return output(c, result)
	}}
	login.Flags().BoolVar(&usePersonal, "use-personal", false, "copy your personal harness login into Dyna once instead of signing in")
	login.Flags().BoolVar(&signIn, "sign-in", false, "sign in with an account without asking about your personal login")
	login.Flags().StringVar(&label, "label", "", "account label")
	login.Flags().StringVar(&accountID, "account", "", "existing private account ID")
	login.Flags().BoolVar(&acpLogin, "acp", false, "sign in to Antigravity's ACP server (Google) in the account's private home")
	login.MarkFlagsMutuallyExclusive("acp", "use-personal")
	login.MarkFlagsMutuallyExclusive("use-personal", "sign-in")
	login.MarkFlagsMutuallyExclusive("use-personal", "account")
	var confirmed bool
	remove := &cobra.Command{Use: "rm <account-id>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
		return engineClient().Call(c.Context(), "RemovePrivateRuntimeAccount", []any{a[0], confirmed}, nil)
	}}
	remove.Flags().BoolVar(&confirmed, "confirm", false, "confirm deletion of this private native account home")
	activate := &cobra.Command{Use: "activate <account-id>", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return engineClient().Call(c.Context(), "ActivatePrivateRuntimeAccount", []any{args[0]}, nil)
	}}
	var importAccount, previewHash string
	preview := &cobra.Command{Use: "config-preview", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		p, err := harnessruntime.New().PreviewPreferenceImport(importAccount)
		if err != nil {
			return err
		}
		return output(c, p)
	}}
	preview.Flags().StringVar(&importAccount, "account", "", "saved account ID")
	_ = preview.MarkFlagRequired("account")
	apply := &cobra.Command{Use: "config-import", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return engineClient().Call(c.Context(), "ImportPrivateConfig", []any{importAccount, previewHash}, nil)
	}}
	apply.Flags().StringVar(&importAccount, "account", "", "saved account ID")
	apply.Flags().StringVar(&previewHash, "preview-hash", "", "hash from config-preview")
	_ = apply.MarkFlagRequired("account")
	_ = apply.MarkFlagRequired("preview-hash")
	accounts.AddCommand(remove, activate)
	root.AddCommand(preview, apply)
	var removeVersion string
	var confirmBundle bool
	removeBundle := &cobra.Command{Use: "rm <harness>", Short: "Remove an unused Dyna-owned runtime bundle", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		pins, err := harnessruntime.OpenPins()
		if err != nil {
			return err
		}
		return harnessruntime.NewBundleManager().Remove(c.Context(), args[0], removeVersion, pins, confirmBundle)
	}}
	removeBundle.Flags().StringVar(&removeVersion, "version", "", "exact private version")
	_ = removeBundle.MarkFlagRequired("version")
	removeBundle.Flags().BoolVar(&confirmBundle, "confirm", false, "confirm removal of this unused private bundle")
	rollback := &cobra.Command{Use: "rollback <harness>", Short: "Select the last good version again after a bad update", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		b, err := harnessruntime.NewBundleManager().Rollback(c.Context(), args[0])
		if err != nil {
			return err
		}
		return output(c, b)
	}}
	root.AddCommand(install, list, doctor, gc, login, accounts, removeBundle, rollback)
	return root
}
