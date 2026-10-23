// The conversations a post can be written in (room.Conversation in Go). A
// client that writes in the room feed names nobody, so the engine stores the
// post in the feed; a client that writes in a DM names that DM.
//
// A session's DM is dm:<session id>. The root seat has no session, so its DM
// names the seat instead, and keeps one conversation when the holder changes.
export const dmConversation = (sessionId: string): string => `dm:${sessionId}`

export const seatDMConversation = (roomId: string): string => `dm:seat:${roomId}`
