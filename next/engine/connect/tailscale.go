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
	find := func(port int) (string, string) {
		for hp, web := range status.Web {
			proxy := strings.TrimSuffix(web.Handlers["/"].Proxy, "/")
			p := strconv.Itoa(port)
			if proxy == "http://127.0.0.1:"+p || proxy == "http://localhost:"+p || proxy == "127.0.0.1:"+p {
				host, _, _ := strings.Cut(hp, ":")
				return host, hp
			}
		}
		return "", ""
	}
	appHost, app := find(appPort)
	if appHost == "" {
		return ""
	}
	return "https://" + strings.TrimSuffix(app, ":443")
}
