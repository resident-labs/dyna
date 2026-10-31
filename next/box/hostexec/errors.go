package hostexec

import "errors"

// ErrHostBusy means the host has no free hostexec slot.
var ErrHostBusy = errors.New("host execution limit of 3 reached; retry later")
