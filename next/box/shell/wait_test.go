package shell

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestWaitOnlyRefusedBeforeStarting(t *testing.T) {
	for _, command := range []string{"sleep 29", " sleep 0.5s ", "sleep '2' && echo ok", "sleep \"2m\"; true", "sleep 1; echo 'still here'", "sleep 1 && echo", "sleep .2; echo \"ok\"", "\nsleep\t2h && true\n", "sleep 0; echo '$(not executed)'", "sleep 0; echo -n ok"} {
		t.Run(command, func(t *testing.T) {
			started := false
			s := New(Config{start: func(_, _, _ string, _ io.Writer) (process, error) { started = true; return nil, errors.New("started") }})
			_, err := s.Run(context.Background(), Run{Command: command})
			if err == nil || !strings.Contains(err.Error(), "sleep only waits. End your turn: Dyna wakes you when a child reports, a job ends or CI finishes.") || started {
				t.Fatalf("started=%v err=%v", started, err)
			}
		})
	}
}

func TestRealWorkIsNotWaitOnly(t *testing.T) {
	for _, command := range []string{"sleep 2 && curl example.com", "echo 'sleep 29'", "echo ok # sleep 29", "sleep 1; echo $(touch marker)", "sleep 1; echo \"$(touch marker)\"", "sleep 1; echo x > marker", "sleep 1; true; make", "sleep 1 &", "sleep 1 || true", "sleep 1\nmake", "sleep 1; echo `make`", "sleep 1; echo $VAR", "sleep 1; true # comment", "sleep 1; echo x;", "sleep 1 2", "env sleep 1"} {
		started := false
		s := New(Config{start: func(_, _, _ string, _ io.Writer) (process, error) { started = true; return nil, errors.New("started") }})
		_, err := s.Run(context.Background(), Run{Command: command})
		if !started || err == nil || err.Error() != "started" {
			t.Errorf("real/ambiguous command refused: %q: %v", command, err)
		}
	}
}
