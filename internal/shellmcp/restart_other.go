//go:build !unix

package shellmcp

import "os"

// wakeup has no implementation outside Unix: a server there never restarts.
type wakeup interface {
	signal()
	wait(in *os.File) bool
	close()
}

func newWakeup() wakeup { return nil }
