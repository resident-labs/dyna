package tools

import "github.com/resident-labs/dyna/contract"

func postEntries() []Entry {
	return []Entry{
		entry("post", "send", contract.MethodPost, typ[contract.PostRequest](), Session, "Send or update a post. Give room and text; use visualUpdate to update a visual. Mentions and attachments are optional.", "post"),
		entry("post", "update_visual", contract.MethodPost, typ[contract.PostRequest](), Session, "Send or update a post. Give room and text; use visualUpdate to update a visual. Mentions and attachments are optional.", "post"),
	}
}
