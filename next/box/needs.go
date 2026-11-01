package box

import "fmt"

// Need is a host capability requested by a toolchain image.
type Need string

const (
	NeedNestedContainers Need = "nested-containers"
	NeedKVM              Need = "kvm"
)

type Image struct {
	Tag   string
	Needs []Need
	// Stack is what a compose file adds to the box. A box with no compose file has none.
	Stack *Stack
}

// MissingDevice identifies a need that the selected host cannot supply.
// ShareDown says a host's mount of the shared folders is down, so a box cannot start there.
type ShareDown struct {
	Host, Reason string
}

func (e ShareDown) Error() string {
	return fmt.Sprintf("the shared folder is not available on host %s: %s", e.Host, e.Reason)
}

type MissingDevice struct {
	Need Need
	Host string
}

func (e MissingDevice) Error() string {
	return fmt.Sprintf("host %s lacks the device for %s", e.Host, e.Need)
}
