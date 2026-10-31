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
}

// MissingDevice identifies a need that the selected host cannot supply.
type MissingDevice struct {
	Need Need
	Host string
}

func (e MissingDevice) Error() string {
	return fmt.Sprintf("host %s lacks the device for %s", e.Host, e.Need)
}
