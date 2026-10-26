package engine

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/connect"
	"dyna-agent/internal/store"
	"dyna-agent/internal/userservices"
	"dyna-agent/internal/websvc"
)

const connectHelp = `dyna engine connect links this computer to a Resident Labs account, so
you can open its web app from anywhere through a managed tunnel and sign
in with your passkey. See docs/desktop/CONNECT.md.

Usage:
  dyna engine connect link [--relay URL] [--name NAME] [--addr ADDR] [--card-addr ADDR]
      Link this computer. It prints a link and a code; approve them on the
      relay page while signed in. The relay makes a tunnel and two names
      (m-<id> and mc-<id> on residentlabs.dev). Then run dyna engine run.
  dyna engine connect status
      Show the link, the owner account, and the Connect sessions.
  dyna engine connect sessions
      List Connect sessions on this computer.
  dyna engine connect revoke ID|DEVICE|all
      End Connect sessions: one session (cs_...), every session of one
      relay device (dev_...), or all of them.
  dyna engine connect unlink [--local]
      Remove the link: the relay deletes the tunnel and names, and every
      Connect session ends. --local forgets the link here when the relay
      cannot be reached; then also unlink the machine on the relay page.

Paired devices (dyna engine run --pair) keep working with or without
a link. These commands refuse to run inside a Dyna agent session unless
you pass --allow-agent.
`

func connectStore() (*connect.Store, error) {
	return connect.OpenStore(filepath.Join(store.DataDir(), "connect"))
}

// TunnelMain runs the managed tunnel connector until the context ends. The
// installed dyna-tunnel.service runs it, so the connector stays up while the
// engine restarts. It returns the exit code.
func TunnelMain(ctx context.Context) int {
	st, err := connectStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		return 1
	}
	log.SetPrefix("dyna-tunnel: ")
	connect.RunTunnel(ctx, st, os.Getenv("DYNA_CLOUDFLARED"), log.Printf)
	return 0
}

// connectMain runs "dyna engine connect". It returns the exit code.
func ConnectMain(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(connectHelp)
		return 0
	}
	cmd, rest := args[0], args[1:]
	fs := flag.NewFlagSet("dyna engine connect "+cmd, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, connectHelp) }
	relay := fs.String("relay", envOr("DYNA_CONNECT_RELAY", connect.DefaultRelay), "relay origin")
	host, _ := os.Hostname()
	name := fs.String("name", host, "name for this computer on the relay")
	addr := fs.String("addr", "127.0.0.1:7457", "the app listener dyna engine run uses")
	cardAddr := fs.String("card-addr", "127.0.0.1:7458", "the card listener dyna engine run uses")
	local := fs.Bool("local", false, "unlink: forget the link here without the relay")
	allowAgent := fs.Bool("allow-agent", false, "run even inside a Dyna agent session")
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if !*allowAgent {
		for _, k := range agentStamps {
			if os.Getenv(k) != "" {
				fmt.Fprintf(os.Stderr, "dyna engine connect: %s is set, so a Dyna agent session started this. Run it from your own terminal, or pass --allow-agent.\n", k)
				return 2
			}
		}
	}
	st, err := connectStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch cmd {
	case "link":
		err = connectLink(ctx, st, *relay, *name, *addr, *cardAddr, os.Stdout)
	case "status":
		err = connectStatus(ctx, st, os.Stdout)
	case "sessions":
		err = printSessions(st, os.Stdout)
	case "revoke":
		if fs.NArg() != 1 {
			err = errors.New("usage: dyna engine connect revoke ID|DEVICE|all")
			break
		}
		var n int
		n, err = st.RevokeSessions(fs.Arg(0))
		if err == nil {
			st.Audit("session.end", fs.Arg(0)+": revoked from the command line")
			fmt.Printf("ended %d Connect session(s)\n", n)
		}
	case "unlink":
		err = connectUnlink(ctx, st, *local, os.Stdout)
	default:
		err = fmt.Errorf("unknown command %q; see dyna engine connect --help", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dyna engine connect:", err)
		return 1
	}
	return 0
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func portOf(addr string) (int, error) {
	p, err := websvc.LoopbackPort(addr)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(p)
}

func connectLink(ctx context.Context, st *connect.Store, relayURL, name, addr, cardAddr string, out io.Writer) error {
	appPort, err := portOf(addr)
	if err != nil {
		return err
	}
	cardPort, err := portOf(cardAddr)
	if err != nil {
		return err
	}
	l, err := st.LinkMachine(ctx, connect.LinkOptions{
		Relay: relayURL, Name: name, AppPort: appPort, CardPort: cardPort,
		Started: func(start connect.LinkStart, fingerprint string) {
			fmt.Fprintf(out, "Open this link in a browser where you are signed in to Resident Labs Connect:\n\n  %s\n\n", start.URL)
			fmt.Fprintf(out, "Check that the page shows the same code and key:\n\n  code  %s\n  key   %s\n\n", start.UserCode, fingerprint)
			fmt.Fprintln(out, "Waiting for approval (10 minutes)...")
		},
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "\nLinked %s to %s's account.\n  app    %s\n  cards  %s\n\n", l.Name, l.Owner.Name, l.App, l.Cards)
	fmt.Fprintln(out, "Now run dyna engine run. It starts the tunnel. Then open the app link on your phone and sign in.")
	return nil
}

func connectStatus(ctx context.Context, st *connect.Store, out io.Writer) error {
	l, ok, err := st.Link()
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(out, "Not linked. Run dyna engine connect link.")
		return nil
	}
	key, err := st.Key()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Machine  %s (%s)\nOwner    %s (%s)\nRelay    %s\nApp      %s\nCards    %s\nKey      %s\nEpoch    %d\nLinked   %s\n",
		l.Name, l.Machine, l.Owner.Name, l.Owner.ID, l.Relay, l.App, l.Cards, connect.Fingerprint(key.Public().(ed25519.PublicKey)), l.Epoch, l.Linked.Format(time.DateTime))
	if l.Unlinked {
		fmt.Fprintln(out, "State    unlinked on the relay; run dyna engine connect unlink --local, then link again")
	} else {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		rl := &connect.Relay{Base: l.Relay, Key: key}
		if info, err := rl.Status(c, l.Machine); err != nil {
			fmt.Fprintf(out, "Relay    %v\n", err)
		} else {
			fmt.Fprintf(out, "State    %s on the relay\n", info.Status)
		}
	}
	fmt.Fprintln(out)
	return printSessions(st, out)
}

func printSessions(st *connect.Store, out io.Writer) error {
	list, err := st.Sessions()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(out, "no Connect sessions")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tDEVICE\tACCOUNT\tBROWSER\tROUTE\tSIGNED IN\tACCESS UNTIL\tENDS")
	for _, s := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Device, s.UserName, s.Name, s.Origin,
			s.Created.Format(time.DateTime), s.Access.Format(time.TimeOnly), s.End.Format(time.DateOnly))
	}
	return tw.Flush()
}

func connectUnlink(ctx context.Context, st *connect.Store, local bool, out io.Writer) error {
	l, ok, err := st.Link()
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(out, "Not linked.")
		return nil
	}
	if !local && !l.Unlinked {
		key, err := st.Key()
		if err != nil {
			return err
		}
		if err := (&connect.Relay{Base: l.Relay, Key: key}).Unlink(ctx, l.Machine); err != nil && !connect.IsRefusal(err) {
			return fmt.Errorf("%w; to forget the link here anyway, pass --local and unlink the machine on the relay page", err)
		}
	}
	if err := st.ClearLink(); err != nil {
		return err
	}
	if err := st.ResetKey(); err != nil {
		return err
	}
	st.Audit("unlink", l.Machine)
	fmt.Fprintf(out, "Unlinked %s. Every Connect session ended. Restart dyna engine run to stop the tunnel.\n", l.Name)
	return nil
}

// connectRefusal says why an engine must not join Resident Labs Connect, or
// "" when it may. Only the live engine joins, and a writable engine is live
// when it runs in its own dyna-engine.service unit. Where no user systemd
// manager runs (a container, some WSL setups), its cgroup is not under
// user@<uid>.service, and it is live on the owner's own data folder. Any
// other engine, such as a read-only QA engine or one started from a shell,
// fails closed.
func connectRefusal(readOnly bool, group, dataDir, ownerDataDir string) string {
	switch {
	case readOnly:
		return "the engine is read-only"
	case callerid.EngineUnit(group):
		return ""
	case strings.Contains(group, "/user@"):
		return "the engine does not run in " + userservices.Engine
	case ownerDataDir == "" || filepath.Clean(dataDir) != filepath.Clean(ownerDataDir):
		return fmt.Sprintf("with no user systemd manager, only an engine on the owner's data folder %s joins, not %s", ownerDataDir, dataDir)
	}
	return ""
}
