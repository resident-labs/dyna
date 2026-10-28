package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"dyna-agent/internal/childenv"
	"dyna-agent/internal/harness/sessionhost"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/profile"
)

// All native protocol adapters attach through one host.
var nativeSessionHost = sessionhost.Default()

// sessionKey names the caller, independent of a protocol's native session
// id. It reports an empty key when no Dyna identity exists, so a protocol
// adapter can key by its own stable native id instead.
func sessionKey(p profile.Profile, execution ExecutionOptions) string {
	if execution.SessionKey != "" {
		return execution.SessionKey
	}
	if execution.Execution.Session != "" {
		return "session/" + execution.Execution.Session
	}
	if execution.Execution.Run != "" {
		return fmt.Sprintf("run/%s/%d", execution.Execution.Run, execution.Execution.Agent)
	}
	if key := p.Env["DYNA_SESSION"]; key != "" {
		return key
	}
	if key := os.Getenv("DYNA_SESSION"); key != "" {
		return key
	}
	return ""
}

// nativeSessionLaunch resolves the same command and runtime protection used
// by ordinary harness attempts, while leaving its lifetime to the host.
func nativeSessionLaunch(p profile.Profile, cwd string, spec commandSpec, execution ExecutionOptions) (sessionhost.Launch, error) {
	if err := validateWorkingDirectory(cwd); err != nil {
		return sessionhost.Launch{}, err
	}
	key := sessionKey(p, execution)
	if key == "" {
		id, err := newSessionID()
		if err != nil {
			return sessionhost.Launch{}, err
		}
		key = id
	}
	cmd, err := harnessCommand(context.Background(), p, cwd, spec.argv, execution)
	if err != nil {
		return sessionhost.Launch{}, err
	}
	env := childenv.WithoutLocks(cmd.Environ())
	argv := append([]string{cmd.Path}, cmd.Args[1:]...)
	return sessionhost.Launch{
		Harness:     p.Harness,
		Key:         key + "\x00" + environmentValue(env, "HOME"),
		Fingerprint: sessionLaunchFingerprint(cmd.Path, env, argv) + "\x00" + cmd.Dir,
		Argv:        argv, Env: env, Dir: cmd.Dir, OnStart: execution.Started, IdleClose: execution.IdleUnload,
		BindLease: nativeSessionBindLease(p),
	}, nil
}

func nativeSessionBindLease(p profile.Profile) func(processguard.Reference) error {
	return func(reference processguard.Reference) error {
		if p.RuntimeBindProcess == nil {
			if p.DynaManaged() {
				return fmt.Errorf("private runtime process protection is missing")
			}
			return nil
		}
		raw, err := reference.MarshalJSON()
		if err != nil {
			return err
		}
		return p.RuntimeBindProcess(raw)
	}
}

// sessionLaunchFingerprint compares launch identity without recording environment
// values in logs. Protocol clients remove only their session selector first.
func sessionLaunchFingerprint(executable string, env, argv []string) string {
	sorted := append([]string(nil), env...)
	sort.Strings(sorted)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00", executable)
	h.Write([]byte(strings.Join(argv, "\x00")))
	h.Write([]byte{0})
	h.Write([]byte(strings.Join(sorted, "\x00")))
	return hex.EncodeToString(h.Sum(nil))
}

func nativeSessionStderr(sess sessionhost.Session) string {
	if local, ok := sess.(sessionhost.LocalExtension); ok {
		return local.StderrTail()
	}
	return ""
}
func haltNativeSession(sess sessionhost.Session) {
	if local, ok := sess.(sessionhost.LocalExtension); ok {
		local.Halt()
	} else {
		_ = sess.Close()
	}
}
func killNativeSession(sess sessionhost.Session) {
	if local, ok := sess.(sessionhost.LocalExtension); ok {
		local.KillGroup()
	} else {
		_ = sess.Close()
	}
}
