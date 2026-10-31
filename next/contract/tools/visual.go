package tools

func visualEntries() []Entry {
	return []Entry{
		local("visual", "check", VisualCheck, typ[FileRequest](), "Check the HTML visual in file.", "visual check"),
	}
}
