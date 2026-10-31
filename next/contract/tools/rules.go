package tools

import "github.com/resident-labs/dyna/contract"

func rulesEntries() []Entry {
	return []Entry{
		entry("rules", "list", contract.MethodRuleList, typ[contract.RuleListRequest](), Session, "List room rules", "rules"),
		entry("rules", "add", contract.MethodRuleAdd, typ[contract.RuleAddRequest](), Tree, "Add a room rule", "rules add"),
		entry("rules", "edit", contract.MethodRuleEdit, typ[contract.RuleEditRequest](), Tree, "Edit a room rule", "rules edit"),
		entry("rules", "remove", contract.MethodRuleRemove, typ[contract.RuleRemoveRequest](), Tree, "Remove a room rule", "rules rm"),
	}
}
