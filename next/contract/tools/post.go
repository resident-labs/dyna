package tools

import "github.com/resident-labs/dyna/contract"

func postEntries() []Entry {
	return []Entry{
		entry("post", "send", contract.MethodPost, typ[contract.PostRequest](), Session, "Send or update a post", "post"),
		entry("post", "update_visual", contract.MethodPost, typ[contract.PostRequest](), Session, "Send or update a post", "post"),
	}
}
