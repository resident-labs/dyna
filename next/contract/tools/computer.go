package tools

func computerEntries() []Entry {
	return []Entry{
		local("computer", "open", ComputerOpen, typ[OpenRequest](), "Open a page", "computer open"),
		local("computer", "click", ComputerClick, typ[SelectorRequest](), "Click an element", "computer click"),
		local("computer", "type", ComputerType, typ[TypeRequest](), "Type into an element", "computer type"),
		local("computer", "text", ComputerText, typ[SelectorRequest](), "Read page text", "computer text"),
		local("computer", "screenshot", ComputerScreenshot, typ[FileRequest](), "Take a screenshot", "computer screenshot"),
	}
}
