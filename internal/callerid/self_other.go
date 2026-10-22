//go:build !linux

package callerid

// enginePID has no peer credentials to read outside Linux.
func enginePID() int { return 0 }
