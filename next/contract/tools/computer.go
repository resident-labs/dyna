package tools

func computerActions() []BoxAction {
	return []BoxAction{
		{Tool: "computer", Name: "open", Handler: ComputerOpen, Request: typ[OpenRequest](), Help: "Open url in the box computer.", CLI: []string{"computer open"}},
		{Tool: "computer", Name: "click", Handler: ComputerClick, Request: typ[SelectorRequest](), Help: "Click the element at selector.", CLI: []string{"computer click"}},
		{Tool: "computer", Name: "type", Handler: ComputerType, Request: typ[TypeRequest](), Help: "Type text into the element at selector.", CLI: []string{"computer type"}},
		{Tool: "computer", Name: "text", Handler: ComputerText, Request: typ[SelectorRequest](), Help: "Read page text. Omit selector to read the whole page.", CLI: []string{"computer text"}},
		{Tool: "computer", Name: "screenshot", Handler: ComputerScreenshot, Request: typ[FileRequest](), Help: "Save a screenshot to file.", CLI: []string{"computer screenshot"}},
	}
}
