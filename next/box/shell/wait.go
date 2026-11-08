package shell

import "regexp"

// Deliberately recognize only this literal grammar, not arbitrary shell programs.
// Decimal GNU sleep durations may be unquoted or wholly single/double quoted.
// Echo words cannot expand, redirect or run anything. Ambiguous syntax is left to bash.
var waitCommand = regexp.MustCompile(`^[ \t\r\n]*sleep[ \t]+(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)[smhd]?[ \t\r\n]*(?:(?:;|&&)[ \t]*(?:true|echo(?:[ \t]+(?:[a-zA-Z0-9_.,:/+-]+|'[^']*'|"[^"$` + "`" + `\\]*"))*)[ \t\r\n]*)?$`)
var quotedDuration = regexp.MustCompile(`^([ \t\r\n]*sleep[ \t]+)(?:'([0-9]+(?:\.[0-9]*)?[smhd]?|\.[0-9]+[smhd]?)'|"([0-9]+(?:\.[0-9]*)?[smhd]?|\.[0-9]+[smhd]?)")`)

func waitOnly(command string) bool {
	return waitCommand.MatchString(quotedDuration.ReplaceAllString(command, "${1}${2}${3}"))
}
