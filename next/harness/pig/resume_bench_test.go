package pig

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/coding"
	"github.com/resident-labs/dyna/contract"
)

// BenchmarkResume separates the two transcript-dependent steps in start. Fixtures
// are synthetic linear histories with escaped source-code tool results, not a
// production replay. No model request or extension binding is timed.
// Run: go test ./harness/pig -run '^$' -bench '^BenchmarkResume$' -benchtime=3x -benchmem
func BenchmarkResume(b *testing.B) { benchmarkResume(b, false) }

// BenchmarkResumeCompacted keeps only the last exchange in the active context.
func BenchmarkResumeCompacted(b *testing.B) { benchmarkResume(b, true) }

func benchmarkResume(b *testing.B, compacted bool) {
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
				callID := fmt.Sprint(count)
				for _, msg := range []map[string]any{
					{"role": "user", "content": "Read the source file", "timestamp": 1767225600000},
					{"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": strings.Repeat("reasoning ", 100)}, map[string]any{"type": "toolCall", "id": callID, "name": "read", "arguments": map[string]string{"path": "/w/main.go"}}}, "api": "anthropic-messages", "provider": "anthropic", "model": "claude-sonnet-4-5", "stopReason": "toolUse", "timestamp": 1767225600000},
					{"role": "toolResult", "toolCallId": callID, "toolName": "read", "content": []any{map[string]any{"type": "text", "text": code}}, "isError": false, "timestamp": 1767225600000},
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
			if compacted {
				write(map[string]any{"type": "compaction", "id": fmt.Sprintf("%08x", count+1), "parentId": parent, "timestamp": "2026-01-01T00:00:00.000Z", "summary": "Earlier source inspections completed.", "firstKeptEntryId": fmt.Sprintf("%08x", count-2), "tokensBefore": 150000})
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
					b.StopTimer()
					if err := created.Session.Close(); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

// BenchmarkResumeFileOpen measures the whole harness Open path without a model
// request. PIG_RESUME_FILE names a private transcript; only scratch copies are
// opened, and copying and Close are excluded from the timed region.
func BenchmarkResumeFileOpen(b *testing.B) {
	source := os.Getenv("PIG_RESUME_FILE")
	if source == "" {
		b.Skip("set PIG_RESUME_FILE to a private transcript")
	}
	dir := b.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.Mkdir(sessions, 0700); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		in, err := os.Open(source)
		if err != nil {
			b.Fatal(err)
		}
		out, err := os.Create(filepath.Join(sessions, "resume.jsonl"))
		if err != nil {
			b.Fatal(err)
		}
		_, err = io.Copy(out, in)
		in.Close()
		out.Close()
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		h, err := Open(context.Background(), Config{Dir: dir, AgentDir: filepath.Join(dir, "agent"), SessionDir: sessions, Model: contract.ModelChoice{Provider: contract.ModelProviderAnthropic, ID: "claude-sonnet-4-5", Thinking: contract.ThinkingLow}, Instructions: "Resume benchmark; no model calls."})
		b.StopTimer()
		if err != nil {
			b.Fatal(err)
		}
		if err := h.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
