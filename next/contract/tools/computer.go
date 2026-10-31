package tools

func computerEntries() []Entry {
	return []Entry{
		local("computer", "open", ComputerOpen, typ[OpenRequest](), "Open url in the box computer.", "computer open"),
		local("computer", "click", ComputerClick, typ[SelectorRequest](), "Click the element at selector.", "computer click"),
		local("computer", "type", ComputerType, typ[TypeRequest](), "Type text into the element at selector.", "computer type"),
		local("computer", "text", ComputerText, typ[SelectorRequest](), "Read page text. Omit selector to read the whole page.", "computer text"),
		local("computer", "screenshot", ComputerScreenshot, typ[FileRequest](), "Save a screenshot to file.", "computer screenshot"),
	}
}
