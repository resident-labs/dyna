package harness

import "strings"

var kimiThinkingLevels = map[string]bool{
	"off": true, "on": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// splitKimiModelEffort separates Dyna's catalog effort variant from the native
// model alias. Unknown suffixes remain part of the model ID, so a custom alias
// containing '#' is not changed accidentally.
func splitKimiModelEffort(model string) (base, effort string) {
	index := strings.LastIndexByte(model, '#')
	if index <= 0 || index == len(model)-1 {
		return model, ""
	}
	candidate := model[index+1:]
	if !kimiThinkingLevels[candidate] {
		return model, ""
	}
	return model[:index], candidate
}
