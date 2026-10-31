package tools

import "github.com/resident-labs/dyna/contract"

func deviceEntries() []Entry {
	return []Entry{
		entry("device", "pair", contract.MethodDevicePair, typ[contract.DevicePairRequest](), Owner, "Pair a device", "device pair"),
		entry("device", "list", contract.MethodDeviceList, typ[contract.DeviceListRequest](), Owner, "List devices", "device list"),
		entry("device", "revoke", contract.MethodDeviceRevoke, typ[contract.DeviceRevokeRequest](), Owner, "Revoke a device", "device revoke"),
		entry("device", "set_scopes", contract.MethodDeviceSetScopes, typ[contract.DeviceSetScopesRequest](), Owner, "Set device scopes", "device set-scopes"),
	}
}
