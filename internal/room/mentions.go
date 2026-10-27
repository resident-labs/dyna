package room

import "strings"

// SplitLeadingMentions separates leading @mention arguments from the rest.
// A mention starts with "@". Mentions end at a bare "--", which is consumed
// and is not part of the rest, or at the first argument that is not a mention.
// Tokens are returned as written, including trailing punctuation. CLI
// arguments and composer drafts both use this rule.
func SplitLeadingMentions(args []string) (mentions, rest []string) {
	i := 0
	for ; i < len(args); i++ {
		if args[i] == "--" {
			i++
			break
		}
		if !strings.HasPrefix(args[i], "@") {
			break
		}
		mentions = append(mentions, args[i])
	}
	return mentions, args[i:]
}

// SplitLeadingMentionText separates leading @mentions from a composer draft.
// It uses SplitLeadingMentions, so a draft and the same words as arguments
// follow one rule. Tokens split on space, tab, or newline. When a mention or
// "--" is consumed, the returned text has its leading whitespace removed.
func SplitLeadingMentionText(text string) (mentions []string, rest string) {
	rest = text
	for rest != "" {
		word := rest
		if i := strings.IndexAny(rest, " \t\n"); i >= 0 {
			word = rest[:i]
		}
		got, left := SplitLeadingMentions([]string{word})
		if len(got) == 0 {
			// An empty remainder means the token was the "--" end marker.
			if len(left) == 0 {
				rest = strings.TrimSpace(strings.TrimPrefix(rest, word))
			}
			break
		}
		mentions = append(mentions, got[0])
		rest = strings.TrimSpace(strings.TrimPrefix(rest, word))
	}
	return mentions, rest
}
