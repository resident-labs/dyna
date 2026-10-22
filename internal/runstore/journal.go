package runstore

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unicode/utf8"

	"dyna-agent/internal/settings"
)

func writeAgentJournalRecord(path string, entry AgentJournalEntry, flags int) error {
	pathAgentID, err := agentIDFromJournalPath(path)
	if err != nil {
		return err
	}
	if entry.AgentID != 0 && entry.AgentID != pathAgentID {
		return fmt.Errorf("agent journal entry id %d does not match path id %d", entry.AgentID, pathAgentID)
	}
	if err := normalizeAgentJournalEntry(&entry); err != nil {
		return err
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode agent journal entry: %w", err)
	}
	b = append(b, '\n')
	if len(b) > agentJournalMaxRecordBytes {
		return fmt.Errorf("agent journal record is too large (maximum %s)", settings.FormatBytes(int64(agentJournalMaxRecordBytes)))
	}
	if err := validateAgentJournalParent(path); err != nil {
		return err
	}
	if flags&os.O_CREATE == 0 {
		if err := validateAgentJournalFile(path); err != nil {
			return err
		}
	}

	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o644)
	if err != nil {
		return fmt.Errorf("open agent journal: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = syscall.Close(fd)
		return fmt.Errorf("open agent journal: invalid file descriptor")
	}
	if info, statErr := f.Stat(); statErr != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		if statErr != nil {
			return fmt.Errorf("inspect agent journal: %w", statErr)
		}
		return fmt.Errorf("agent journal must be a regular file")
	}
	n, writeErr := f.Write(b)
	if writeErr == nil && n != len(b) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return fmt.Errorf("append agent journal: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close agent journal: %w", closeErr)
	}
	return nil
}

func normalizeAgentJournalEntry(entry *AgentJournalEntry) error {
	entry.Kind = strings.TrimSpace(entry.Kind)
	entry.Message = strings.TrimSpace(entry.Message)
	entry.Next = strings.TrimSpace(entry.Next)
	if entry.TS <= 0 {
		return fmt.Errorf("agent journal timestamp must be positive")
	}
	if entry.Source != "agent" && entry.Source != "system" {
		return fmt.Errorf("agent journal source must be agent or system")
	}
	if err := validateJournalKind(entry.Kind); err != nil {
		return err
	}
	if entry.Message == "" {
		return fmt.Errorf("journal message must not be empty")
	}
	if !utf8.ValidString(entry.Message) {
		return fmt.Errorf("journal message must be valid UTF-8")
	}
	if len(entry.Message) > agentJournalMaxMessageBytes {
		return fmt.Errorf("journal message is too long (maximum %s)", settings.FormatBytes(int64(agentJournalMaxMessageBytes)))
	}
	if !utf8.ValidString(entry.Next) {
		return fmt.Errorf("journal next step must be valid UTF-8")
	}
	if len(entry.Next) > agentJournalMaxMessageBytes {
		return fmt.Errorf("journal next step is too long (maximum %s)", settings.FormatBytes(int64(agentJournalMaxMessageBytes)))
	}
	return nil
}

func validateJournalKind(kind string) error {
	if kind == "" {
		return fmt.Errorf("journal kind must not be empty")
	}
	if len(kind) > agentJournalMaxKindBytes {
		return fmt.Errorf("journal kind is too long (maximum %s)", settings.FormatBytes(int64(agentJournalMaxKindBytes)))
	}
	for i, c := range []byte(kind) {
		if (c >= 'a' && c <= 'z') || (i > 0 && c >= '0' && c <= '9') || (i > 0 && (c == '-' || c == '_')) {
			continue
		}
		return fmt.Errorf("journal kind must start with a lowercase letter and contain only lowercase letters, digits, '-' or '_'")
	}
	return nil
}
