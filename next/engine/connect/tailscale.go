package connect

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// TailscaleRoute discovers existing Serve mappings; it never changes them.
func TailscaleRoute(ctx context.Context, appPort int) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tailscale", "serve", "status", "--json")
	cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return parseTailscaleServe(out, appPort)
}

func parseTailscaleServe(out []byte, appPort int) string {
	var status struct {
		Web map[string]struct {
			Handlers map[string]struct{ Proxy string }
		}
	}
	if json.Unmarshal(out, &status) != nil {
		return ""
	}
	port := strconv.Itoa(appPort)
	for hostPort, web := range status.Web {
		switch strings.TrimSuffix(web.Handlers["/"].Proxy, "/") {
		case "http://127.0.0.1:" + port, "http://localhost:" + port, "127.0.0.1:" + port:
			return "https://" + strings.TrimSuffix(hostPort, ":443")
		}
	}
	return ""
}
