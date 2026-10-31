package tools

func visualEntries() []Entry {
	return []Entry{
		local("visual", "check", VisualCheck, typ[FileRequest](), "Check a visual", "visual check"),
	}
}
