package workeroutput

// IsCompaction recognizes only verified stdout protocol records.
func IsCompaction(decl *Declaration, line string) bool {
	return DecodeLine(decl, line).Compaction
}
