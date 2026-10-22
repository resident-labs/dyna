package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGrokInstallPinsNativeBinaryAndPrivateInstallerHome(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("npm installation needs Node.js")
	}
	platform := map[string]string{"linux/amd64": "linux-x64", "linux/arm64": "linux-arm64", "darwin/amd64": "darwin-x64", "darwin/arm64": "darwin-arm64"}[runtime.GOOS+"/"+runtime.GOARCH]
	if platform == "" {
		t.Skip("no supported npm platform")
	}
	shared := t.TempDir()
	t.Setenv("GROK_HOME", shared)
	if err := os.MkdirAll(filepath.Join(shared, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "bin/grok"), []byte("#!/bin/sh\necho shared-other\n"), 0700); err != nil {
		t.Fatal(err)
	}
	m := manager(t)
	m.Run = func(ctx context.Context, args, env []string) error {
		if args[0] == "curl" {
			return fixtureInstall(ctx, args, env)
		}
		if args[0] == "node" {
			return runBundleCommand(ctx, args, env)
		}
		if !strings.Contains(strings.Join(args, " "), "--ignore-scripts") {
			t.Fatal("Grok postinstall can write outside its bundle")
		}
		for _, item := range env {
			if strings.HasPrefix(item, "GROK_HOME=") && !inside(m.Root, strings.TrimPrefix(item, "GROK_HOME=")) {
				t.Fatal("inherited Grok install home", item)
			}
		}
		root := args[4]
		version := strings.TrimPrefix(args[len(args)-1], "@xai-official/grok@")
		dir := filepath.Join(root, "lib/node_modules/@xai-official/grok/node_modules/@xai-official/grok-"+platform, "bin")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		payload := fmt.Sprintf("#!/bin/sh\necho %s\n", version)
		cmd := exec.CommandContext(ctx, node, "-e", `const fs=require('node:fs'); const zlib=require('node:zlib'); fs.writeFileSync(process.argv[1],zlib.brotliCompressSync(Buffer.from(process.argv[2])));`, filepath.Join(dir, "grok.br"), payload)
		cmd.Env = env
		return cmd.Run()
	}
	old := install(t, m, "grok", "1.0.61")
	latest := install(t, m, "grok", "1.0.62")
	for _, b := range []Bundle{old, latest} {
		got, err := exec.CommandContext(t.Context(), b.Executable).Output()
		if err != nil || strings.TrimSpace(string(got)) != b.Version {
			t.Fatal("bundle did not run its pinned native binary", b.Version, string(got), err)
		}
	}
	if old.Executable == latest.Executable {
		t.Fatal("versions share an executable")
	}
}
