package eval

import (
	"bufio"
	"fmt"
	"io"
)

// WriteReport lists every task, including skipped and unmeasured tasks.
func WriteReport(w io.Writer, results []CellResult, skipRepository, skipChildren bool) error {
	expected := make(map[Cell]bool)
	for _, cell := range Schedule(skipRepository, skipChildren) {
		expected[cell] = true
	}
	recorded := make(map[Cell]CellResult)
	for _, result := range results {
		if !expected[result.Cell] {
			return fmt.Errorf("unexpected sanity result: %+v", result.Cell)
		}
		if _, ok := recorded[result.Cell]; ok {
			return fmt.Errorf("duplicate sanity result: %+v", result.Cell)
		}
		recorded[result.Cell] = result
	}
	out := bufio.NewWriter(w)
	fmt.Fprint(out, "Cache share is cache-read tokens divided by all input tokens.\n\n")
	for _, arm := range []Arm{Caller, Declared} {
		passed, scored := 0, 0
		fmt.Fprintf(out, "## %s\n\n| Task | Result | Total tokens | Cache share |\n|---:|---|---:|---:|\n", arm)
		for _, task := range tasks {
			cell := Cell{Task: task.ID, Provider: Claude, Arm: arm}
			status, total, share := "not run", "—", "—"
			if expected[cell] {
				status = "unmeasured"
				if result, ok := recorded[cell]; ok {
					if result.Scored {
						scored++
						status = "wrong"
						if result.Score.Correct {
							passed++
							status = "pass"
						}
					}
					if result.Measured && len(result.Attempts) > 0 {
						usage := result.Attempts[len(result.Attempts)-1].Usage
						if usage.Input >= 0 && usage.CacheRead >= 0 && usage.CacheWrite >= 0 && usage.Output >= 0 {
							total = fmt.Sprint(usage.Total())
							input := usage.Input + usage.CacheRead + usage.CacheWrite
							if input > 0 {
								share = fmt.Sprintf("%.1f%%", 100*float64(usage.CacheRead)/float64(input))
							}
						}
					}
				}
			}
			fmt.Fprintf(out, "| %d | %s | %s | %s |\n", task.ID, status, total, share)
		}
		fmt.Fprintf(out, "\nAccuracy: %d/%d scored tasks. Missing tasks are not counted.\n\n", passed, scored)
	}
	return out.Flush()
}
