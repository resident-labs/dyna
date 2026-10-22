package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/exec"

	"dyna-agent/internal/agentslice"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
)

func main() {
	flags := flag.NewFlagSet("dyna-desktop", flag.ExitOnError)
	addr := flags.String("addr", "127.0.0.1:7457", "the loopback app listener used by dyna engine run")
	_ = flags.Parse(os.Args[1:])
	if flags.NArg() != 0 {
		log.Fatal("dyna-desktop takes only --addr")
	}
	// Parse flags first so --help needs neither systemd nor a GUI.
	executable, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	cmd := exec.Command(executable, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if handled, err := agentslice.RunForeground(cmd, "desktop"); handled {
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	page := desktopPage(*addr)
	// Do not set BindingsAllowedOrigins. Wails rejects IPC from the server
	// origin, so the web app and its card frames cannot call the native runtime.
	err = wails.Run(&options.App{
		Title:            "Dyna",
		Width:            1440,
		Height:           900,
		MinWidth:         960,
		MinHeight:        600,
		BackgroundColour: &options.RGBA{R: 0x1c, G: 0x1b, B: 0x1e, A: 0xff},
		AssetServer: &assetserver.Options{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
		})},
		OnStartup: func(context.Context) { startWindowHost(*addr) },
		Linux:     &linux.Options{ProgramName: "dyna-desktop", WebviewGpuPolicy: linux.WebviewGpuPolicyOnDemand},
	})
	if err != nil {
		log.Fatal(err)
	}
}
