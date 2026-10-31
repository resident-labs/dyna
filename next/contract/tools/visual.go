package tools

func visualActions() []BoxAction {
	return []BoxAction{
		{Tool: "visual", Name: "check", Handler: VisualCheck, Request: typ[FileRequest](), Help: "Check the HTML visual in file.", CLI: []string{"visual check"}},
	}
}
