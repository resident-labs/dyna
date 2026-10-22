package computers

import (
	"fmt"
	"strings"
)

// ValidateAppCommand keeps web navigation on the shared browser path.
func ValidateAppCommand(command []string) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return fmt.Errorf("a command is required")
	}
	executable := strings.ToLower(command[0])
	if strings.Contains(executable, "://") || strings.HasPrefix(executable, "about:") || strings.HasPrefix(executable, "data:") {
		return fmt.Errorf("for web pages, use dyna browser open <url>")
	}
	return nil
}
