// Package managed installs one shared Linux environment with systemd-nspawn.
package managed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/childenv"
)

const marker = "# Managed by Dyna managed install\n"

type options struct {
	name, image, code, sha256 string
	port, cardPort            int
}
type runner struct {
	ctx      context.Context
	out, err io.Writer
}

func (r runner) sudo(input io.Reader, args ...string) error {
	fmt.Fprint(r.out, "+ sudo")
	for _, a := range args {
		fmt.Fprintf(r.out, " %s", strconv.Quote(a))
	}
	fmt.Fprintln(r.out)
	c := exec.CommandContext(r.ctx, "sudo", args...)
	if input == nil {
		input = os.Stdin
	}
	c.Stdin, c.Stdout, c.Stderr = input, r.out, r.err
	if err := childenv.Clean(c).Run(); err != nil {
		return fmt.Errorf("%s: %w", args[0], err)
	}
	return nil
}
func (r runner) write(path, content string) error {
	f, err := os.CreateTemp("", "dyna-managed-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return r.sudo(nil, "install", "-D", "-m", "0644", f.Name(), path)
}
func NewInstallCommand() *cobra.Command   { return command(false) }
func NewUninstallCommand() *cobra.Command { return command(true) }
func command(remove bool) *cobra.Command {
	o := options{name: "dyna", port: 7467, cardPort: 7468}
	home, _ := os.UserHomeDir()
	o.code = filepath.Join(home, "code")
	var managed bool
	verb := "install"
	if remove {
		verb = "uninstall"
	}
	c := &cobra.Command{Use: verb, Short: verb + " the managed Linux environment", Args: cobra.NoArgs}
	c.Flags().BoolVar(&managed, "managed", false, "use the shared systemd-nspawn environment")
	c.Flags().StringVar(&o.name, "name", o.name, "container name")
	if !remove {
		c.Flags().StringVar(&o.image, "image", "", "rootfs tar path or HTTPS URL (required)")
		c.Flags().StringVar(&o.sha256, "sha256", "", "expected image SHA-256 (downloads otherwise use <URL>.sha256)")
		c.Flags().StringVar(&o.code, "code", o.code, "host code folder to bind at the same path")
		c.Flags().IntVar(&o.port, "port", o.port, "loopback web port")
		c.Flags().IntVar(&o.cardPort, "card-port", o.cardPort, "loopback card port")
	}
	c.RunE = func(c *cobra.Command, _ []string) error {
		if !managed {
			return fmt.Errorf("use %s --managed; see docs/managed-install.md", verb)
		}
		if runtime.GOOS != "linux" {
			return fmt.Errorf("managed install requires Linux with systemd-nspawn")
		}
		if os.Geteuid() == 0 {
			return fmt.Errorf("run dyna as your normal user; it uses sudo for each system step")
		}
		if err := validateName(o.name); err != nil {
			return err
		}
		r := runner{c.Context(), c.OutOrStdout(), c.ErrOrStderr()}
		if remove {
			return uninstall(r, o)
		}
		var err error
		o.code, err = filepath.Abs(o.code)
		if err != nil {
			return err
		}
		if err = validate(o); err != nil {
			return err
		}
		return install(r, o)
	}
	return c
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)

func validateName(s string) error {
	if !nameRE.MatchString(s) {
		return fmt.Errorf("name must contain 1 to 63 letters, digits or hyphens, and start with a letter or digit")
	}
	return nil
}
func validate(o options) error {
	if err := validateName(o.name); err != nil {
		return err
	}
	if o.sha256 != "" {
		if b, err := hex.DecodeString(o.sha256); err != nil || len(b) != sha256.Size {
			return fmt.Errorf("--sha256 must contain exactly 64 hexadecimal digits")
		}
	}
	if o.image == "" {
		return fmt.Errorf("--image is required; use a trusted Dyna rootfs tar")
	}
	if o.port < 1 || o.port > 65535 || o.cardPort < 1 || o.cardPort > 65535 || o.port == o.cardPort {
		return fmt.Errorf("ports must be different numbers from 1 to 65535")
	}
	if !filepath.IsAbs(o.code) || o.code == "/" || strings.TrimSpace(o.code) != o.code || strings.ContainsAny(o.code, ":\r\n\\\"%") {
		return fmt.Errorf("--code must be an absolute folder path without trailing space, colon, quote, backslash, percent or line breaks")
	}
	for _, ch := range o.code {
		if ch < 32 {
			return fmt.Errorf("--code contains a control character")
		}
	}
	// Do not mount over the image's operating system, home, or Dyna state.
	for _, base := range []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/dev", "/proc", "/sys", "/run", "/var", "/home/dyna"} {
		if o.code == base || strings.HasPrefix(o.code, base+"/") {
			return fmt.Errorf("--code cannot be inside %s", base)
		}
	}
	return nil
}
func nspawnPath(name string) string { return "/etc/systemd/nspawn/" + name + ".nspawn" }
func dropinPath(name string) string {
	return "/etc/systemd/system/systemd-nspawn@" + name + ".service.d/dyna.conf"
}
func nspawnConfig(o options, dri bool) string {
	s := marker + "[Exec]\nBoot=yes\nPrivateUsers=no\n\n[Network]\nVirtualEthernet=no\n\n[Files]\nBind=" + o.code + ":" + o.code + "\n"
	if dri {
		s += "Bind=/dev/dri\n"
	}
	return s
}
func dropin(dri bool) string {
	s := marker + "[Service]\nCPUWeight=20\nMemoryHigh=65%\nMemoryMax=80%\n"
	if dri {
		s += "DeviceAllow=char-drm rw\n"
	}
	return s
}
func importArgs(tool, image, name string) []string {
	args := []string{tool, "import-tar"}
	if tool == "importctl" {
		args = append(args, "--class=machine")
	}
	return append(args, image, name)
}
func install(r runner, o options) error {
	for _, tool := range []string{"sudo", "machinectl", "systemd-nspawn", "systemctl", "install", "flock"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s is required; install your distribution's systemd container tools", tool)
		}
	}
	// Import refuses an existing image even when /var/lib/machines is root-only.
	for _, path := range []string{nspawnPath(o.name), dropinPath(o.name), "/var/lib/machines/" + o.name, "/var/lib/machines/" + o.name + ".raw"} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("%s already exists; choose another name or uninstall the managed environment", path)
		} else if !os.IsNotExist(err) && !(strings.HasPrefix(path, "/var/lib/machines/") && os.IsPermission(err)) {
			return err
		}
	}
	for _, port := range []int{o.port, o.cardPort} {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return fmt.Errorf("port %d is unavailable: %w", port, err)
		}
		l.Close()
	}
	if err := os.MkdirAll(o.code, 0755); err != nil {
		return err
	}
	image, cleanup, err := stageImage(r.ctx, o.image, o.sha256, r.err)
	if err != nil {
		return err
	}
	defer cleanup()
	importer := "importctl"
	if _, err := exec.LookPath(importer); err != nil {
		importer = "machinectl"
	}
	if err := r.sudo(nil, importArgs(importer, image, o.name)...); err != nil {
		return err
	}
	_, err = os.Stat("/dev/dri")
	dri := err == nil
	// The marker also makes a partial install safe to remove with uninstall.
	if err := r.write(nspawnPath(o.name), nspawnConfig(o, dri)); err != nil {
		return fmt.Errorf("image imported; remove it with machinectl remove %s: %w", o.name, err)
	}
	fail := func(err error) error {
		return fmt.Errorf("partial install; run dyna uninstall --managed --name %s before retrying: %w", o.name, err)
	}
	if err := r.write(dropinPath(o.name), dropin(dri)); err != nil {
		return fail(err)
	}
	args := []string{"systemd-nspawn", "--quiet", "--directory=/var/lib/machines/" + o.name, "--register=no", "--settings=no", "--private-users=no", "--as-pid2", "--pipe", "/bin/sh", "-s", "--", strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid()), strconv.Itoa(o.port), strconv.Itoa(o.cardPort), o.code}
	if err := r.sudo(strings.NewReader(configureScript), args...); err != nil {
		return fail(err)
	}
	if err := r.sudo(nil, "systemctl", "daemon-reload"); err != nil {
		return fail(err)
	}
	if err := r.sudo(strings.NewReader(enableMachineScript), "sh", "-s", "--", o.name); err != nil {
		return fail(err)
	}
	if err := r.sudo(nil, "machinectl", "start", o.name); err != nil {
		return fail(err)
	}
	fmt.Fprintf(r.out, "Managed environment started: http://127.0.0.1:%d\nGet a pairing code:\n  sudo machinectl shell dyna@%s /usr/local/bin/dyna engine run --pair\n", o.port, o.name)
	return nil
}

// Run before boot, without host code or credentials mounted. No host path enters shell code.
const configureScript = `set -eu
uid=$1 gid=$2 port=$3 card=$4 code=$5
test -x /usr/local/bin/dyna
test -x /usr/local/bin/dyna-desktop
test -f /etc/dyna/image-release
test -f /etc/systemd/user/dyna-engine.service
old_uid=$(id -u dyna)
old_gid=$(id -g dyna)
if [ "$old_gid" != "$gid" ]; then groupmod -g "$gid" dyna; fi
if [ "$old_uid" != "$uid" ]; then usermod -u "$uid" dyna; fi
usermod -g "$gid" dyna
chown -R "$uid:$gid" /home/dyna
mkdir -p /etc/dyna /var/lib/systemd/linger
touch /var/lib/systemd/linger/dyna
printf 'DYNA_SERVE_PORT=%s\nDYNA_CARD_PORT=%s\n' "$port" "$card" > /etc/dyna/serve.env
mkdir -p /home/dyna/.config/systemd/user/default.target.wants
ln -s /etc/systemd/user/dyna-engine.service /home/dyna/.config/systemd/user/default.target.wants/dyna-engine.service
if [ ! -e /home/dyna/code ] && [ ! -L /home/dyna/code ]; then ln -s "$code" /home/dyna/code; fi
chown -R "$uid:$gid" /home/dyna/.config
`

func uninstall(r runner, o options) error {
	b, err := os.ReadFile(nspawnPath(o.name))
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(b), marker) {
		return fmt.Errorf("refusing to remove %s: it is not a Dyna managed environment", o.name)
	}
	// Stop waits for the container to exit before the image is removed.
	if err := r.sudo(nil, "systemctl", "stop", "systemd-nspawn@"+o.name+".service"); err != nil {
		return err
	}
	if err := r.sudo(nil, "machinectl", "disable", o.name); err != nil {
		return err
	}
	if err := r.sudo(nil, "machinectl", "remove", o.name); err != nil {
		return err
	}
	if err := r.sudo(nil, "rm", "-f", "--", nspawnPath(o.name), dropinPath(o.name)); err != nil {
		return err
	}
	if err := r.sudo(nil, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := r.sudo(strings.NewReader(cleanupTargetScript), "sh", "-s"); err != nil {
		return err
	}
	fmt.Fprintln(r.out, "Removed the managed environment and its internal data. The bound code folder is unchanged.")
	return nil
}
func stageImage(ctx context.Context, source, expected string, warnings io.Writer) (string, func(), error) {
	noop := func() {}
	u, err := url.Parse(source)
	if err != nil {
		return "", noop, err
	}
	if u.Scheme == "" {
		path, err := filepath.Abs(source)
		if err != nil {
			return "", noop, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", noop, err
		}
		if !info.Mode().IsRegular() {
			return "", noop, fmt.Errorf("image must be a regular tar file")
		}
		if expected == "" {
			fmt.Fprintln(warnings, "Warning: local image has no --sha256; its integrity has not been verified.")
		} else if err := verifyImage(path, expected); err != nil {
			return "", noop, err
		}
		return path, noop, nil
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", noop, fmt.Errorf("image URL must use HTTPS without user information")
	}
	client := &http.Client{Timeout: 30 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) > 10 {
			return fmt.Errorf("unsafe image redirect")
		}
		return nil
	}}
	if expected == "" {
		checksumURL := *u
		checksumURL.Path += ".sha256"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumURL.String(), nil)
		if err != nil {
			return "", noop, err
		}
		res, err := client.Do(req)
		if err != nil {
			return "", noop, fmt.Errorf("download image checksum: %w", err)
		}
		b, readErr := io.ReadAll(io.LimitReader(res.Body, 4097))
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return "", noop, fmt.Errorf("image checksum download: %s; provide --sha256", res.Status)
		}
		if readErr != nil {
			return "", noop, readErr
		}
		expected, err = parseChecksum(string(b), filepath.Base(u.Path))
		if err != nil {
			return "", noop, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return "", noop, err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", noop, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", noop, fmt.Errorf("image download: %s", res.Status)
	}
	f, err := os.CreateTemp("", "dyna-rootfs-*.tar")
	if err != nil {
		return "", noop, err
	}
	cleanup := func() { os.Remove(f.Name()) }
	_, err = io.Copy(f, res.Body)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return "", noop, err
	}
	if err := verifyImage(f.Name(), expected); err != nil {
		cleanup()
		return "", noop, err
	}
	return f.Name(), cleanup, nil
}

func parseChecksum(text, filename string) (string, error) {
	if len(text) > 4096 {
		return "", fmt.Errorf("image checksum file is too large")
	}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != filename {
			continue
		}
		b, err := hex.DecodeString(fields[0])
		if err == nil && len(b) == sha256.Size {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("image checksum file has no valid SHA-256 for %s", filename)
}

func verifyImage(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), expected) {
		return fmt.Errorf("image SHA-256 mismatch; refusing to import")
	}
	return nil
}

// Shared state survives removal of the first Dyna machine. Serialize changes
// made by Dyna installers; systemd still owns the actual enablement links.
const enableMachineScript = `set -eu
exec 9>/run/lock/dyna-managed.lock
flock 9
state=/etc/systemd/nspawn/.dyna-machines-target
status=$(systemctl is-enabled machines.target) || test "$?" = 1
if [ "$status" = disabled ]; then
 printf '%s\n' 'Enabled by Dyna' > "$state"
fi
machinectl enable "$1"
`

// Fail closed: retain ownership and target enablement if either query fails.
// Dependencies include enabled machines even when they are stopped.
const cleanupTargetScript = `set -eu
exec 9>/run/lock/dyna-managed.lock
flock 9
state=/etc/systemd/nspawn/.dyna-machines-target
test -f "$state" || exit 0
deps=$(systemctl show --property=Wants --property=Requires --value machines.target)
set -f
for unit in $deps; do
 # systemd adds this mount for /var/lib/machines even with no containers.
 test "$unit" = var-lib-machines.mount || exit 0
done
machines=$(machinectl list --no-legend --no-pager)
test -z "$machines" || exit 0
systemctl disable machines.target
rm -f -- "$state"
`
