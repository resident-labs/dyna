package podman

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/box"
)

// ServiceSliceName is the slice every service runs in, under dyna.slice by its name.
const ServiceSliceName = "dyna-svc.slice"

// ServiceSlice is the file of that slice. CPUWeight keeps a service's reserved CPU whatever runs beside it.
const ServiceSlice = "[Unit]\nDescription=Dyna services\n\n[Slice]\nCPUWeight=1000\n"

// unitWord writes one value as a systemd word: in double quotes, with the characters systemd reads inside them escaped.
// A specifier (%) and a variable ($) would otherwise expand.
func unitWord(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$").Replace(s) + `"`
}

// RenderServiceUnit writes a service's quadlet file (spec 5.18). It holds the names of the secrets and never their values, and
// every isolation promise is one of its lines. Declared values hold no control character (ServiceSpec.Parse), so none can start a line.
func RenderServiceUnit(u box.ServiceUnitSpec) string {
	p := u.Parts
	name := box.ServiceName(u.ID)
	memory := strconv.FormatInt(p.MemBytes, 10)
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	line("[Unit]")
	line("Description=Dyna service %s of room %s", p.Spec.Name, u.Room)
	line("StartLimitIntervalSec=600")
	line("StartLimitBurst=5")
	line("")
	line("[Container]")
	line("ContainerName=%s", name)
	line("Image=%s", u.Image)
	if p.Digest != "" {
		line("Pull=missing")
	} else {
		line("Pull=never")
	}
	line("Label=dyna.service=%s", u.ID)
	line("UserNS=nomap")
	line("Network=pasta:--no-map-gw")
	line("DropCapability=all")
	line("NoNewPrivileges=true")
	line("ReadOnly=true")
	line("ReadOnlyTmpfs=false")
	line("PodmanArgs=--image-volume=ignore")
	line("Tmpfs=/tmp:rw,size=64m,mode=1777")
	line("PodmanArgs=--cpus=%s --memory=%s --memory-swap=%s --pids-limit=%d", strconv.FormatFloat(float64(p.CPUMilli)/1000, 'f', -1, 64), memory, memory, p.Pids())
	for _, port := range p.Ports {
		line("PublishPort=%d:%d/%s", port.Host, port.Container, port.Proto)
	}
	if p.Spec.Data != "" {
		line("Volume=%s:%s", name, p.Spec.Data)
	}
	for _, key := range sortedKeys(p.Spec.Env) {
		line("Environment=%s", unitWord(key+"="+p.Spec.Env[key]))
	}
	for _, secret := range p.Spec.Secrets {
		line("Secret=%s,type=env,target=%s", box.ServiceSecretName(u.ID, secret), secret)
	}
	if len(p.Spec.Command) > 0 {
		words := make([]string, len(p.Spec.Command))
		for i, word := range p.Spec.Command {
			words[i] = unitWord(word)
		}
		line("Exec=%s", strings.Join(words, " "))
	}
	line("")
	line("[Service]")
	line("Slice=%s", ServiceSliceName)
	line("Restart=%s", p.Spec.Restart)
	line("")
	line("[Install]")
	line("WantedBy=default.target")
	return b.String()
}

// ServiceUnitSHA is the hash of a quadlet file, which the engine compares after a restart.
func ServiceUnitSHA(file string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(file))) }

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
