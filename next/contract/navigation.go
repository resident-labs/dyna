package contract

import "time"

// The navigation methods are read-only and a session may call them (7.9). Feeds, sessions and rooms are readable across
// rooms; DMs, transcripts and files only in the caller's own room. The owner has no room limit.
const (
	MethodSearchPosts      Method = "search.posts"
	MethodSearchSessions   Method = "search.sessions"
	MethodSearchRooms      Method = "search.rooms"
	MethodHistoryRead      Method = "history.read"
	MethodTranscriptDigest Method = "transcript.digest"
)

// Limits of the navigation tools. A page is at most NavPageBytes of JSON, under the 10 KB where the harness cuts tool output.
const (
	NavPageBytes         = 8 << 10
	SearchLimitDefault   = 10
	SearchLimitMax       = 25
	SearchSnippetChars   = 240
	SearchGoalChars      = 200
	HistoryLimitDefault  = 15
	HistoryLimitMax      = 50
	HistoryTextChars     = 800
	HistoryPostBytes     = 32 << 10
	TranscriptLimitDef   = 2
	TranscriptLimitMax   = 5
	TranscriptTextChars  = 800
	TranscriptInputChars = 300
	TranscriptStepChars  = 160
	SearchRoomHere       = "here"
	SearchWordsMax       = 8
	SearchQueryCharsMax  = 200
	TranscriptChunkBytes = 8 << 10
)

// SearchRequest is shared by the three search actions.
type SearchRequest struct {
	Query string `json:"query,omitempty"` // 1 to 8 words, at most 200 characters, each a prefix; rooms may omit it
	Room  string `json:"room,omitempty"`  // a name or ID; "here" is the caller's room; empty is every room
	From  string `json:"from,omitempty" desc:"For posts: a session name, or owner."`
	After string `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"` // default 10, at most 25
}

type SearchHit struct {
	ID      EventID   `json:"id"`
	Room    string    `json:"room"`
	Home    string    `json:"home"` // feed, or dm:<session name>
	From    string    `json:"from"` // owner, a session name, or "<name> in <room>" for a cross-room post
	At      time.Time `json:"at"`
	Snippet string    `json:"snippet"` // at most 240 characters, the matched words in [ ]
	Files   int       `json:"files,omitempty"`
}

type SessionHit struct {
	ID     SessionID `json:"id"`
	Name   string    `json:"name"`
	Room   string    `json:"room"`
	Parent string    `json:"parent,omitempty"`
	State  string    `json:"state"` // open, working, closed or failed
	Goal   string    `json:"goal,omitempty"`
}

type RoomHit struct {
	ID       RoomID    `json:"id"`
	Name     string    `json:"name"`
	Root     string    `json:"root,omitempty"`
	Open     int       `json:"open"`
	Working  int       `json:"working"`
	LastPost time.Time `json:"lastPost,omitzero"`
}

type SearchResponse struct {
	Posts    []SearchHit  `json:"posts,omitempty"`
	Sessions []SessionHit `json:"sessions,omitempty"`
	Rooms    []RoomHit    `json:"rooms,omitempty"`
	Total    int          `json:"total"`
	Next     string       `json:"next,omitempty"`
}

type HistoryRequest struct {
	Room   string  `json:"room,omitempty" desc:"A room name or ID; omit for yours."`
	DM     string  `json:"dm,omitempty" desc:"A session name or ID: read the owner's DM with it, in your own room."`
	Around EventID `json:"around,omitempty"`
	Before EventID `json:"before,omitempty"`
	After  EventID `json:"after,omitempty"`
	Post   EventID `json:"post,omitempty"`
	Limit  int     `json:"limit,omitempty"` // default 15, at most 50
}

type HistoryPost struct {
	ID      EventID   `json:"id"`
	At      time.Time `json:"at"`
	From    string    `json:"from"`
	To      []string  `json:"to,omitempty"`
	ReplyTo EventID   `json:"replyTo,omitempty"`
	Text    string    `json:"text"`
	Cut     bool      `json:"cut,omitempty"`
	Files   []string  `json:"files,omitempty"` // "report.html (text/html, 14 KB)"
}

type HistoryResponse struct {
	Room  string        `json:"room"`
	Home  string        `json:"home"`
	Posts []HistoryPost `json:"posts"`
	Older EventID       `json:"older,omitempty"`
	Newer EventID       `json:"newer,omitempty"`
}

type TranscriptDigestRequest struct {
	Session string `json:"session" desc:"A session name or ID of your own room."`
	Turn    TurnID `json:"turn,omitempty"`
	Before  TurnID `json:"before,omitempty"`
	Item    string `json:"item,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	Limit   int    `json:"limit,omitempty"` // default 2, at most 5
}

type DigestTurn struct {
	ID      TurnID    `json:"id"`
	Started time.Time `json:"started"`
	Outcome string    `json:"outcome"` // ok, failed, stopped or running
	In      []string  `json:"in"`
	Steps   []string  `json:"steps"`
	Text    string    `json:"text,omitempty"`
	Cut     []string  `json:"cut,omitempty"` // item IDs that were cut
}

type TranscriptDigestResponse struct {
	Session    string       `json:"session"`
	Room       string       `json:"room"`
	Turns      []DigestTurn `json:"turns,omitempty"`
	Next       TurnID       `json:"next,omitempty"`
	Chunk      string       `json:"chunk,omitempty"`
	NextOffset int          `json:"nextOffset,omitempty"`
}
