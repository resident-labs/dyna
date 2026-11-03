package contract

import "time"

const (
	MethodDevicePair      Method = "device.pair"
	MethodDeviceList      Method = "device.list"
	MethodDeviceScopes    Method = "device.scopes"
	MethodDeviceRevoke    Method = "device.revoke"
	MethodDeviceSetScopes Method = "device.set_scopes"
	MethodDeviceSetHost   Method = "device.set_host"
)

type Device struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	PairedAt   time.Time `json:"pairedAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Scopes     []Scope   `json:"scopes"`
	Current    bool      `json:"current"`        // the device that made this call
	Host       HostName  `json:"host,omitempty"` // the host this device is on, if the owner named one
}
type DevicePairRequest struct {
	Scopes []Scope `json:"scopes"`
}
type DevicePairResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expiresAt"`
	Routes    []string  `json:"routes"` // configured public origins; a loopback page cannot name one itself
}
type DeviceListRequest struct{}
type DeviceListResponse struct {
	Devices []Device `json:"devices"`
}

// DeviceScopesRequest asks for the caller's own scopes. Any scope may ask.
type DeviceScopesRequest struct{}
type DeviceScopesResponse struct {
	Scopes []Scope  `json:"scopes"`
	Host   HostName `json:"host,omitempty"` // the host this device is on, if the owner named one
}
type DeviceRevokeRequest struct {
	ID string `json:"id"`
}
type DeviceRevokeResponse struct{}
type DeviceSetScopesRequest struct {
	ID     string  `json:"id"`
	Scopes []Scope `json:"scopes"`
}
type DeviceSetScopesResponse struct{}

// DeviceSetHostRequest names the host a paired device is on, so the owner's inputs from it say so. No host clears it.
type DeviceSetHostRequest struct {
	ID   string   `json:"id"`
	Host HostName `json:"host"`
}
type DeviceSetHostResponse struct{}
