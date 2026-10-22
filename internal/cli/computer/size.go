package computer

import (
	"dyna-agent/internal/computers"
	"github.com/spf13/cobra"
)

func requestedSize(c *cobra.Command, raw, intent string) (*computers.Size, string, error) {
	if !c.Flags().Changed("size") {
		return nil, intent, nil
	}
	size, err := computers.ParseSize(raw)
	if err != nil {
		return nil, "", err
	}
	intent, err = computers.RequiredStatus(intent)
	return &size, intent, err
}

func applySize(c *cobra.Command, id string, size *computers.Size, intent string) error {
	if size == nil {
		return nil
	}
	var state computers.Status
	return computers.NewClient(id).Call(c.Context(), computers.Request{Op: "size", Size: size, Status: intent}, &state)
}
