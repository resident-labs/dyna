package pig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/coding"
)

// BenchmarkResume separates the two transcript-dependent steps in start. Fixtures
// are synthetic linear histories with escaped source-code tool results, not a
// production replay. No model request or extension binding is timed.
// Run: go test ./harness/pig -run '^$' -bench '^BenchmarkResume$' -benchtime=3x -benchmem
func BenchmarkResume(b *testing.B) {
	for _, mib := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("%dMiB", mib), func(b *testing.B) {
			dir := b.TempDir()
			path := filepath.Join(dir, "session.jsonl")
			f, err := os.Create(path)
			if err != nil {
				b.Fatal(err)
			}
			write := func(v any) {
				if err := json.NewEncoder(f).Encode(v); err != nil {
					b.Fatal(err)
				}
			}
			write(map[string]any{"type": "session", "version": 3, "id": "bulk", "timestamp": "2026-01-01T00:00:00.000Z", "cwd": dir})
			code := strings.Repeat("func main() {\n\tfmt.Println(\"hello, world\")\n}\n// héllo 日本語\n", 300)
			parent := ""
			count := 0
			for {
				for _, msg := range []map[string]any{
					{"role": "user", "content": "Read the source file", "timestamp": 1767225600000},
					{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": strings.Repeat("reasoning ", 100)}, map[string]any{"type": "toolCall", "id": fmt.Sprint(count), "name": "read", "arguments": map[string]string{"path": "/w/main.go"}}}, "api": "anthropic-messages", "provider": "anthropic", "model": "claude-sonnet-4-5", "stopReason": "toolUse", "timestamp": 1767225600000},
					{"role": "toolResult", "toolCallId": fmt.Sprint(count), "toolName": "read", "content": []any{map[string]any{"type": "text", "text": code}}, "isError": false, "timestamp": 1767225600000},
				} {
					id := fmt.Sprintf("%08x", count+1)
					var p any = parent
					if parent == "" {
						p = nil
					}
					write(map[string]any{"type": "message", "id": id, "parentId": p, "timestamp": "2026-01-01T00:00:00.000Z", "message": msg})
					parent = id
					count++
				}
				pos, err := f.Seek(0, 1)
				if err != nil {
					b.Fatal(err)
				}
				if pos >= int64(mib)*1024*1024 {
					break
				}
			}
			if err := f.Close(); err != nil {
				b.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}
			services, err := coding.CreateAgentSessionServices(coding.CreateAgentSessionServicesOptions{CWD: dir, AgentDir: filepath.Join(dir, "agent")})
			if err != nil {
				b.Fatal(err)
			}
			defer services.Close()
			model, err := coding.BuildModel("anthropic/claude-sonnet-4-5", services)
			if err != nil {
				b.Fatal(err)
			}
			b.Run("manager", func(b *testing.B) {
				b.SetBytes(info.Size())
				b.ReportAllocs()
				for b.Loop() {
					if _, err := coding.SessionManagerFor(services, coding.SessionStartOptions{SessionDir: dir, ResumePath: path}); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("create", func(b *testing.B) {
				b.SetBytes(info.Size())
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					manager, err := coding.SessionManagerFor(services, coding.SessionStartOptions{SessionDir: dir, ResumePath: path})
					if err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
					created, err := coding.CreateAgentSessionFromServices(coding.CreateAgentSessionFromServicesOptions{Services: services, SessionManager: manager, Model: model, Tools: []string{"+codemode", "+bash"}, ContextUpdates: true, ActiveEffortAtTopLevel: true})
					if err != nil {
						b.Fatal(err)
					}
					if err := created.Session.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
