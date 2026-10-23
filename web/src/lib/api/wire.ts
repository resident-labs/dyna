// Shapes of the JSON the engine sends to the web client. Each type follows
// the Go struct named in its comment. Go sends time.Time as an ISO string
// and []byte as base64. A nil Go slice or map without omitempty arrives as
// null, so those fields are nullable here. omitempty omits a zero string,
// number, bool, slice or map, so those fields are optional here. It never
// omits a time.Time: an unset one is "0001-01-01T00:00:00Z", so it is
// required. omitzero omits a zero time.Time, so that field is optional.

// ApiError is the body of a failed call; code is "renew" when a Connect
// session needs to renew. internal/websvc/http.go, server.go.
export interface ApiError {
  error: string
  code?: string
}

// Page is internal/listpage Page[T].
export interface Page<T> {
  items: T[] | null
  older: boolean
  cursor: string
}

// CardRef is websvc.CardRef, the answer of CardURL: where an HTML card loads
// and when its ticket ends.
export interface CardRef {
  url: string
  expires: string
}
