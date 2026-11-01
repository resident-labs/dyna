package compose

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

const example = `
services:
  agent:
    build: .dyna/images/rust
    environment:
      DATABASE_URL: postgres://app:dev@db:5432/app
    depends_on:
      db:
        condition: service_healthy
    volumes:
      - shared:/data/shared
  cache:
    image: ghcr.io/example/cache:1
    depends_on: [db]
    command: cache --port 7000
  db:
    build:
      context: ./.dyna/images/pg
    environment:
      - POSTGRES_PASSWORD=dev
      - PRICE=$$5
    command: ["postgres", "-c", "fsync=off"]
    volumes:
      - data:/var/lib/postgresql/data
      - type: volume
        source: shared
        target: /shared
        read_only: true
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 2s
      timeout: 5s
      retries: 10
      start_period: 10s
    cpus: 1.5
    mem_limit: 1g
    pids_limit: 256
volumes:
  data:
  shared: {}
`

func TestParseAcceptsTheAllowedKeys(t *testing.T) {
	file, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	agent := file.Agent
	if agent.Build != "rust" || len(agent.Env) != 1 || agent.Env[0].Name != "DATABASE_URL" || !reflect.DeepEqual(agent.Volumes, []box.Volume{{Name: "shared", Target: "/data/shared"}}) {
		t.Fatalf("agent %+v", agent)
	}
	if len(file.Sidecars) != 2 || file.Sidecars[0].Name != "db" || file.Sidecars[1].Name != "cache" {
		t.Fatalf("a sidecar must follow what it depends on: %+v", file.Sidecars)
	}
	db, cache := file.Sidecars[0], file.Sidecars[1]
	if db.Build != "pg" || !reflect.DeepEqual(db.Command, []string{"postgres", "-c", "fsync=off"}) {
		t.Fatalf("db %+v", db)
	}
	if !reflect.DeepEqual(db.Env, []box.EnvVar{{Name: "POSTGRES_PASSWORD", Value: "dev"}, {Name: "PRICE", Value: "$5"}}) {
		t.Fatalf("db env %+v", db.Env)
	}
	if !reflect.DeepEqual(db.Volumes, []box.Volume{{Name: "data", Target: "/var/lib/postgresql/data"}, {Name: "shared", Target: "/shared", ReadOnly: true}}) {
		t.Fatalf("db volumes %+v", db.Volumes)
	}
	if !reflect.DeepEqual(db.Health, []string{"CMD-SHELL", "pg_isready -U postgres"}) || db.HealthTimeout != 5*time.Second || db.Wait != 30*time.Second {
		t.Fatalf("db health %+v", db)
	}
	if db.CPUs != 1.5 || db.MemBytes != 1<<30 || db.Pids != 256 {
		t.Fatalf("db limits %+v", db)
	}
	if cache.Image != "ghcr.io/example/cache:1" || !reflect.DeepEqual(cache.Command, []string{"cache", "--port", "7000"}) {
		t.Fatalf("cache %+v", cache)
	}
	if !reflect.DeepEqual(file.Builds(), []string{"rust", "pg"}) {
		t.Fatalf("builds %v", file.Builds())
	}
}

func TestParseNormalizesImagesSoAFlagCannotPassAsOne(t *testing.T) {
	file, err := Parse([]byte("services:\n  agent: {}\n  db:\n    image: postgres:16\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := file.Sidecars[0].Image; got != "docker.io/library/postgres:16" {
		t.Fatalf("image %q", got)
	}
}

func TestAnAgentOnlyFileNeedsNoSidecar(t *testing.T) {
	file, err := Parse([]byte("services:\n  agent:\n    build: .dyna/images/rust\n"))
	if err != nil {
		t.Fatal(err)
	}
	if file.Agent.Build != "rust" || len(file.Sidecars) != 0 {
		t.Fatalf("%+v", file)
	}
}

// sidecar puts lines under a service called db, beside a plain agent.
func sidecar(lines ...string) string {
	var b strings.Builder
	b.WriteString("services:\n  agent: {}\n  db:\n    image: postgres:16\n")
	for _, line := range lines {
		b.WriteString("    " + line + "\n")
	}
	return b.String()
}

func agent(lines ...string) string {
	var b strings.Builder
	b.WriteString("services:\n  agent:\n")
	for _, line := range lines {
		b.WriteString("    " + line + "\n")
	}
	return b.String()
}

func TestParseRefusesEveryKeyThatIsNotAllowed(t *testing.T) {
	for _, key := range []string{
		// What the owner named.
		"privileged: true", "cap_add: [SYS_ADMIN]", "devices: [/dev/kvm]", "security_opt: ['seccomp=unconfined']",
		"ports: ['5432:5432']", "userns_mode: host", "network_mode: host", "pid: host", "ipc: host",
		// What else could reach the host or the box.
		"uts: host", "cgroup: host", "cgroup_parent: dyna.slice", "cap_drop: [ALL]", "group_add: [docker]", "user: root",
		"sysctls: ['net.ipv4.ip_forward=1']", "ulimits: {nofile: 1}", "tmpfs: [/run]", "shm_size: 1g", "runtime: runc",
		"volumes_from: [agent]", "links: [agent]", "external_links: [other]", "network_mode: container:other", "networks: [default]",
		"extra_hosts: ['a:1.1.1.1']", "dns: [1.1.1.1]", "dns_search: [a]", "dns_opt: [a]", "mac_address: '00:00:00:00:00:00'",
		"hostname: host", "domainname: a", "container_name: box_other", "expose: [5432]",
		"env_file: /etc/passwd", "secrets: [a]", "configs: [a]", "extends: {file: /etc/passwd, service: a}", "label_file: /etc/passwd",
		"credential_spec: {file: a}", "gpus: all", "blkio_config: {weight: 1}", "isolation: a", "use_api_socket: true",
		"oom_kill_disable: true", "oom_score_adj: -1000", "cpu_shares: 1", "cpuset: '0'", "cpu_rt_runtime: 1", "memswap_limit: 1g", "mem_reservation: 1g",
		"deploy: {resources: {limits: {cpus: '1'}}}", "develop: {}", "profiles: [a]", "scale: 2", "pull_policy: always", "platform: linux/amd64",
		"entrypoint: /bin/sh", "working_dir: /", "read_only: true", "init: true", "restart: always", "stop_signal: SIGKILL", "stop_grace_period: 1s",
		"tty: true", "stdin_open: true", "attach: true", "labels: {a: b}", "annotations: {a: b}", "logging: {driver: none}",
		"post_start: [{command: id}]", "pre_stop: [{command: id}]", "provider: {type: a}", "models: [a]",
		// Unknown keys, and an extension.
		"x-anything: 1", "made_up: 1", "Image: other",
	} {
		t.Run(key, func(t *testing.T) {
			name, _, _ := strings.Cut(key, ":")
			_, err := Parse([]byte(sidecar(key)))
			var refused box.StackError
			if !errors.As(err, &refused) || !strings.Contains(err.Error(), name+" is not allowed") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestParseRefusesTopLevelKeysThatAreNotAllowed(t *testing.T) {
	for _, key := range []string{"version: '3'", "name: x", "include: [/etc/passwd]", "secrets: {a: {file: /etc/passwd}}", "configs: {a: {file: /etc/passwd}}", "networks: {a: {}}", "models: {a: {}}", "x-common: {a: b}", "made_up: 1"} {
		t.Run(key, func(t *testing.T) {
			name, _, _ := strings.Cut(key, ":")
			_, err := Parse([]byte(agent("environment: {A: b}") + key + "\n"))
			if err == nil || !strings.Contains(err.Error(), name+" is not allowed") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestParseRefuses(t *testing.T) {
	big := "services:\n  agent: {}\n"
	for i := range maxServices {
		big += fmt.Sprintf("  s%d:\n    image: a\n", i)
	}
	for name, tc := range map[string]struct{ file, want string }{
		"no agent":         {"services:\n  db:\n    image: a\n", "no service named agent"},
		"second document":  {"services:\n  agent: {}\n---\nservices:\n  db:\n    image: a\n    privileged: true\n", "one YAML document"},
		"no services":      {"volumes: {}\n", "services"},
		"not YAML":         {"services: [", "not valid YAML"},
		"too large":        {strings.Repeat("#", MaxFileBytes+1), "over"},
		"too many":         {big, "services; a file may have"},
		"service name":     {"services:\n  agent: {}\n  Db.x:\n    image: a\n", "lowercase"},
		"alias bomb":       {"a: &a [x,x,x,x,x,x,x,x,x]\nb: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a]\nc: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b]\nd: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c]\ne: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d]\nf: &f [*e,*e,*e,*e,*e,*e,*e,*e,*e]\ng: &g [*f,*f,*f,*f,*f,*f,*f,*f,*f]\nh: [*g,*g,*g,*g,*g,*g,*g,*g,*g]\n", ""},
		"image and build":  {sidecar("build: .dyna/images/a"), "either image or build"},
		"neither":          {"services:\n  agent: {}\n  db:\n    command: id\n", "either image or build"},
		"bind":             {sidecar("volumes: ['/etc:/etc']"), "not a named volume"},
		"relative bind":    {sidecar("volumes: ['./x:/x']"), "not a named volume"},
		"parent bind":      {sidecar("volumes: ['../x:/x']"), "not a named volume"},
		"home bind":        {sidecar("volumes: ['~/.ssh:/x']"), "not a named volume"},
		"long bind":        {sidecar("volumes: [{type: bind, source: /etc, target: /etc}]"), "not a named volume"},
		"long tmpfs":       {sidecar("volumes: [{type: tmpfs, target: /x}]"), "not a named volume"},
		"long image":       {sidecar("volumes: [{type: image, source: a, target: /x}]"), "not a named volume"},
		"bind settings":    {sidecar("volumes: [{type: volume, source: a, target: /x, bind: {propagation: rshared}}]"), "bind is not allowed"},
		"volume settings":  {sidecar("volumes: [{type: volume, source: a, target: /x, volume: {subpath: a}}]"), "volume is not allowed"},
		"anonymous":        {sidecar("volumes: [/data]"), "not a volume this file declares"},
		"undeclared":       {sidecar("volumes: ['other:/data']"), "not a volume this file declares"},
		"option":           {sidecar("volumes: ['data:/data:z']") + "volumes: {data: }\n", "ro or rw"},
		"colon in target":  {sidecar("volumes: [{type: volume, source: data, target: '/a:/b'}]") + "volumes: {data: }\n", "plain absolute path"},
		"relative target":  {sidecar("volumes: [{type: volume, source: data, target: 'a'}]") + "volumes: {data: }\n", "plain absolute path"},
		"driver option":    {agent("environment: {A: b}") + "volumes:\n  data:\n    driver_opts: {type: none, o: bind, device: /etc}\n", "a volume is only a name"},
		"driver":           {agent("environment: {A: b}") + "volumes:\n  data:\n    driver: local\n", "a volume is only a name"},
		"external":         {agent("environment: {A: b}") + "volumes:\n  data:\n    external: true\n", "a volume is only a name"},
		"another's volume": {agent("environment: {A: b}") + "volumes:\n  data:\n    name: dyna-box-box_other\n", "a volume is only a name"},
		"volume name":      {agent("environment: {A: b}") + "volumes:\n  Data.x:\n", "lowercase"},
		"passthrough list": {sidecar("environment: [GH_TOKEN]"), "would pass the host's through"},
		"passthrough map":  {sidecar("environment: {GH_TOKEN: }"), "would pass the host's through"},
		"bad env name":     {sidecar("environment: {'A B': c}"), "not a variable name"},
		"braced variable":  {sidecar("environment: {A: '${GH_TOKEN}'}"), "variable GH_TOKEN"},
		"plain variable":   {sidecar("environment: ['A=$GH_TOKEN']"), "variable GH_TOKEN"},
		"default variable": {sidecar("environment: {A: '${GH_TOKEN:-x}'}"), "variable GH_TOKEN"},
		"variable command": {sidecar("command: echo ${HOME}"), "variable HOME"},
		"variable image":   {"services:\n  agent: {}\n  db:\n    image: ${A}\n", "variable A"},
		"variable volume":  {sidecar("volumes: ['${HOME}:/x']"), "variable HOME"},
		"local image":      {"services:\n  agent: {}\n  db:\n    image: localhost/dyna-base:abc\n", "only a registry image"},
		"local registry":   {"services:\n  agent: {}\n  db:\n    image: localhost:5000/a\n", "only a registry image"},
		"flag as image":    {"services:\n  agent: {}\n  db:\n    image: --privileged\n", "image"},
		"image ID":         {"services:\n  agent: {}\n  db:\n    image: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n", "image"},
		"build outside":    {"services:\n  agent: {}\n  db:\n    build: .\n", "build must be a folder"},
		"build traversal":  {"services:\n  agent: {}\n  db:\n    build: .dyna/images/../../etc\n", "build must be a folder"},
		"build root":       {"services:\n  agent: {}\n  db:\n    build: .dyna/images\n", "build must be a folder"},
		"build nested":     {"services:\n  agent: {}\n  db:\n    build: .dyna/images/a/b\n", "build must be a folder"},
		"build absolute":   {"services:\n  agent: {}\n  db:\n    build: /etc\n", "build must be a folder"},
		"build URL":        {"services:\n  agent: {}\n  db:\n    build: https://example.com/a.git\n", "build must be a folder"},
		"build dockerfile": {"services:\n  agent: {}\n  db:\n    build: {context: .dyna/images/a, dockerfile: /etc/passwd}\n", "dockerfile is not allowed"},
		"build args":       {"services:\n  agent: {}\n  db:\n    build: {context: .dyna/images/a, args: {A: b}}\n", "args is not allowed"},
		"build secrets":    {"services:\n  agent: {}\n  db:\n    build: {context: .dyna/images/a, secrets: [a]}\n", "secrets is not allowed"},
		"build ssh":        {"services:\n  agent: {}\n  db:\n    build: {context: .dyna/images/a, ssh: [default]}\n", "ssh is not allowed"},
		"build contexts":   {"services:\n  agent: {}\n  db:\n    build: {context: .dyna/images/a, additional_contexts: {a: /etc}}\n", "additional_contexts is not allowed"},
		"no test":          {sidecar("healthcheck: {interval: 1s}"), "test must be a command"},
		"test none":        {sidecar("healthcheck: {test: [NONE]}"), "test must be a command"},
		"health disable":   {sidecar("healthcheck: {test: [CMD, a], disable: true}"), "disable is not allowed"},
		"negative pids":    {sidecar("pids_limit: -1"), "negative"},
		"depends on agent": {sidecar("depends_on: [agent]"), "nothing can depend on agent"},
		"unknown depend":   {sidecar("depends_on: [other]"), "other"},
		"self depend":      {sidecar("depends_on: [db]"), "cycle"},
		"cycle":            {"services:\n  agent: {}\n  a:\n    image: a\n    depends_on: [b]\n  b:\n    image: a\n    depends_on: [a]\n", "cycle"},
		"completed":        {"services:\n  agent: {}\n  a:\n    image: a\n  b:\n    image: a\n    depends_on: {a: {condition: service_completed_successfully}}\n", "service_started and service_healthy"},
		"no health to see": {"services:\n  agent: {}\n  a:\n    image: a\n  b:\n    image: a\n    depends_on: {a: {condition: service_healthy}}\n", "no healthcheck"},
		"depend restart":   {"services:\n  agent: {}\n  a:\n    image: a\n  b:\n    image: a\n    depends_on: {a: {condition: service_started, restart: true}}\n", "restart is not allowed"},
		"depend required":  {"services:\n  agent: {}\n  a:\n    image: a\n  b:\n    image: a\n    depends_on: {a: {condition: service_started, required: false}}\n", "required is not allowed"},
		"agent image":      {agent("image: postgres:16"), "image is not allowed"},
		"agent command":    {agent("command: bash"), "command is not allowed"},
		"agent health":     {agent("healthcheck: {test: [CMD, a]}"), "healthcheck is not allowed"},
		"agent cpus":       {agent("cpus: 1"), "cpus is not allowed"},
		"agent memory":     {agent("mem_limit: 1g"), "mem_limit is not allowed"},
		"agent HOME":       {agent("environment: {HOME: /etc}"), "HOME is set by Dyna"},
		"agent cache":      {agent("environment: [GOCACHE=/tmp]"), "GOCACHE is set by Dyna"},
		"agent over box":   {agent("volumes: ['vol:/box']") + "volumes: {vol: }\n", "/box"},
		"agent over share": {agent("volumes: ['vol:/box/shared']") + "volumes: {vol: }\n", "/box"},
		"agent over run":   {agent("volumes: ['vol:/run']") + "volumes: {vol: }\n", "/run/dyna"},
		"agent over bin":   {agent("volumes: ['vol:/opt/dyna/bin']") + "volumes: {vol: }\n", "/opt/dyna"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.file))
			var refused box.StackError
			if !errors.As(err, &refused) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

// The agent may name its own limits nowhere, but a sidecar may use a volume target that would be Dyna's in a box.
func TestReservedPathsAndNamesAreTheBoxsOwn(t *testing.T) {
	file, err := Parse([]byte(sidecar("environment: {HOME: /var/lib/db}", "volumes: ['vol:/box']") + "volumes: {vol: }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if file.Sidecars[0].Env[0].Name != "HOME" || file.Sidecars[0].Volumes[0].Target != "/box" {
		t.Fatalf("%+v", file.Sidecars[0])
	}
}

func TestCheckLimitsHoldsAServiceWithinTheSession(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeAgent) // 1 CPU, 2 GiB, 1024 pids
	for name, tc := range map[string]struct {
		line string
		ok   bool
	}{
		"cpus at":      {"cpus: 1", true},
		"cpus over":    {"cpus: 1.5", false},
		"memory at":    {"mem_limit: 2g", true},
		"memory over":  {"mem_limit: 3g", false},
		"pids at":      {"pids_limit: 1024", true},
		"pids over":    {"pids_limit: 1025", false},
		"no limit set": {"command: id", true},
	} {
		t.Run(name, func(t *testing.T) {
			file, err := Parse([]byte(sidecar(tc.line)))
			if err != nil {
				t.Fatal(err)
			}
			if err := file.CheckLimits(size); (err == nil) != tc.ok {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestStackHashFollowsWhatRuns(t *testing.T) {
	file, err := Parse([]byte(sidecar("command: id")))
	if err != nil {
		t.Fatal(err)
	}
	first := file.Stack("base:1", map[string]string{"db": "image:1"})
	if first.Hash == "" || !reflect.DeepEqual(first.Names, []string{"agent", "db"}) || first.Infra != "base:1" {
		t.Fatalf("%+v", first)
	}
	if same := file.Stack("base:1", map[string]string{"db": "image:1"}); same.Hash != first.Hash {
		t.Fatal("one input gave two hashes")
	}
	if other := file.Stack("base:1", map[string]string{"db": "image:2"}); other.Hash == first.Hash {
		t.Fatal("a new sidecar image did not change the stack")
	}
	if other := file.Stack("base:2", map[string]string{"db": "image:1"}); other.Hash == first.Hash {
		t.Fatal("a new base did not change the stack")
	}
}
