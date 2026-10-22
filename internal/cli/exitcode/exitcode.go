// Package exitcode lets CLI commands return a specific process exit status.
package exitcode

type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) ExitCode() int { return e.Code }

func New(code int, msg string) error {
	return &Error{Code: code, Msg: msg}
}
