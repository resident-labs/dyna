package tools

import "github.com/resident-labs/dyna/contract"

func hostexecEntries() []Entry {
	return []Entry{
		entry("hostexec", "run", contract.MethodHostexecRun, typ[contract.HostexecRunRequest](), Grant, "Run argv on host with a grant. cwd and timeout are optional.", "hostexec run"),
		operation("hostexec", "request_grant", contract.HostexecGrantArgs{}.OpKind(), typ[contract.HostexecGrantArgs](), Session, "Request access to hosts with a reason. Omit room and to to use your own; omit expires for the default duration.", "hostexec grant"),
		entry("hostexec", "revoke", contract.MethodHostexecRevoke, typ[contract.HostexecRevokeRequest](), Owner, "Revoke a host grant", "hostexec revoke"),
	}
}
