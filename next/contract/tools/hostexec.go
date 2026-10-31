package tools

import "github.com/resident-labs/dyna/contract"

func hostexecEntries() []Entry {
	return []Entry{
		entry("hostexec", "run", contract.MethodHostexecRun, typ[contract.HostexecRunRequest](), Grant, "Run a host command", "hostexec run"),
		operation("hostexec", "request_grant", contract.HostexecGrantArgs{}.OpKind(), typ[contract.HostexecGrantArgs](), Session, "Request a host grant", "hostexec grant"),
		entry("hostexec", "revoke", contract.MethodHostexecRevoke, typ[contract.HostexecRevokeRequest](), Owner, "Revoke a host grant", "hostexec revoke"),
	}
}
