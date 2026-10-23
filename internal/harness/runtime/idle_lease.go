package runtime

// ReleaseIdle releases a turn's account protection after its native work has
// settled or stopped. Unlike Close, it does not retain protection for an idle
// process. A runner that crashes before this call still leaves its bound
// process reference for account admission and recovery.
func (l *ResourceLease) ReleaseIdle() error {
	if l == nil {
		return nil
	}
	if err := l.Close(); err != nil {
		return err
	}
	removeResourceLease(l.path)
	return nil
}
