// Package outside owns the single outside-text trust boundary for Dyna.
// Callers attach a typed Origin describing where text came from; Wrap is the
// one function that formats the <outside> boundary at delivery.
package outside

import (
	"regexp"
	"strings"
)

// outsideTag matches the start of an outside tag in any letter case, even with
// spaces or zero-width characters after < or /.
var outsideTag = regexp.MustCompile(`(?i)<([\s\x{200b}-\x{200d}\x{feff}]*/?[\s\x{200b}-\x{200d}\x{feff}]*outside)`)

// attributeValue drops characters that would end or split an attribute value.
var attributeValue = strings.NewReplacer("\"", "", "<", "", ">", "", "\n", " ", "\r", " ")

// Origin names where text came from and whether it is trusted.
// Only Trusted() is delivered plain; a zero-value Origin fails closed as outside text.
type Origin struct {
	trusted bool
	source  string
	author  string
}

// Trusted marks text written by the owner or within the caller's own room.
func Trusted() Origin { return Origin{trusted: true} }

// GitHub marks text written by a GitHub actor outside the owner's account.
// An empty actor omits the author attribute (as for read-only GitHub MCP results).
func GitHub(actor string) Origin {
	if actor != "" && !strings.HasPrefix(actor, "@") {
		actor = "@" + actor
	}
	return Origin{source: "github", author: actor}
}

// Web marks text from a web search result, attributed to the page's domain.
func Web(domain string) Origin {
	return Origin{source: "web", author: domain}
}

// Room marks text originating in another room.
func Room(roomName, author string) Origin {
	return Origin{source: "room " + roomName, author: author}
}

// Feed marks an inbound message from a room's feed service.
func Feed(service, author string) Origin {
	return Origin{source: service, author: author}
}

// Service marks diagnostic text emitted by a service container (such as failure details).
func Service(name string) Origin {
	return Origin{source: "service " + name}
}

// Subscription marks output produced by a subscription run.
func Subscription(id string) Origin {
	return Origin{source: "subscription " + id}
}

// DiscordChannel marks text from a named Discord channel.
func DiscordChannel(channelName, author string) Origin {
	return Origin{source: "discord #" + channelName, author: author}
}

// DiscordChannelID marks messages read from a Discord channel by ID.
func DiscordChannelID(channelID string) Origin {
	return Origin{source: "discord channel " + channelID}
}

// DiscordDirectory marks channel names listed from the Discord directory.
func DiscordDirectory() Origin {
	return Origin{source: "discord directory"}
}

// DiscordAttachments marks attachment metadata from a Discord user.
func DiscordAttachments(author, authorID string) Origin {
	return Origin{source: "discord attachments", author: author + " (" + authorID + ")"}
}

// DiscordServer marks server-level names from a Discord guild event.
func DiscordServer(serverID string) Origin {
	return Origin{source: "discord server " + serverID}
}

// MCP marks a tool result returned by an MCP server other than dyna.
// The built-in github MCP server uses the github source.
func MCP(server string) Origin {
	if server == "github" {
		return GitHub("")
	}
	return Origin{source: "mcp " + server}
}

// ToolCall marks failed tool-call arguments included in a diagnostic notice.
func ToolCall() Origin { return Origin{source: "tool call"} }

// ToolResult marks failed tool-call error text included in a diagnostic notice.
func ToolResult() Origin { return Origin{source: "tool result"} }

// Trusted reports whether the origin is trusted and delivered unwrapped.
func (o Origin) Trusted() bool { return o.trusted }

// Source returns the source label that Wrap places in the outside tag.
func (o Origin) Source() string { return o.source }

// Author returns the author label that Wrap places in the outside tag.
func (o Origin) Author() string { return o.author }

// Wrap applies the single outside-text boundary at delivery (decision 13).
// Trusted origins pass through unchanged. Every other origin is wrapped in
// <outside source="…" author="…">…</outside> with inner tags neutralized.
func Wrap(origin Origin, text string) string {
	if origin.trusted {
		return text
	}
	source := strings.TrimSpace(attributeValue.Replace(origin.source))
	if source == "" {
		source = "outside"
	}
	open := `<outside source="` + source + `"`
	if author := strings.TrimSpace(attributeValue.Replace(origin.author)); author != "" {
		open += ` author="` + author + `"`
	}
	return open + ">\n" + outsideTag.ReplaceAllString(text, "‹${1}") + "\n</outside>"
}
