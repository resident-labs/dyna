package runtime

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/engine/ci"
)

type ciFrameRunner struct {
	reply []byte
}

func (r *ciFrameRunner) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	if !slices.Contains(args, "bash") && !strings.Contains(args[len(args)-1], "dyna-frame") {
		return nil, nil
	}
	return append(slices.Clone(r.reply), []byte(fmt.Sprintf("DYNA-END %020d %03d\n", len(r.reply), 0))...), nil
}

func TestCIHostListRunnersClassifiesEgressBinary(t *testing.T) {
	runner := &ciFrameRunner{}
	dir, err := os.MkdirTemp("", "dyna-ci-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	h, err := podman.New("user@pve", dir, "pve", runner, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	currentSum := strings.Repeat("a", 64)
	oldSum := strings.Repeat("b", 64)
	boxes := &Boxes{}
	boxes.binOnce.Do(func() { boxes.binSum = currentSum })
	driver := ciHost{Host: h, name: "pve", boxes: boxes}

	for _, tc := range []struct {
		name  string
		reply string
		want  ci.EgressState
	}{
		{"absent", `[]`, ci.EgressAbsent},
		{"stopped current binary", `[{"Names":["dyna-ci-egress"],"State":"exited","Labels":{"dyna.ci":"egress","dyna.binary":"` + currentSum + `"}}]`, ci.EgressStopped},
		{"running legacy without binary label", `[{"Names":["dyna-ci-egress"],"State":"running","Labels":{"dyna.ci":"egress"}}]`, ci.EgressStale},
		{"running older binary", `[{"Names":["dyna-ci-egress"],"State":"running","Labels":{"dyna.ci":"egress","dyna.binary":"` + oldSum + `"}}]`, ci.EgressStale},
		{"running current binary", `[{"Names":["dyna-ci-egress"],"State":"running","Labels":{"dyna.ci":"egress","dyna.binary":"` + currentSum + `"}}]`, ci.EgressCurrent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner.reply = []byte(tc.reply)
			_, got, err := driver.ListRunners(context.Background(), false, nil)
			if err != nil || got != tc.want {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
