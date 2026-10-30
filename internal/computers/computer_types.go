package computers

import "time"

type Stream struct {
	Running bool   `json:"running"`
	Codec   string `json:"codec"`
	Encoder string `json:"encoder"`
}

type ActionIntent struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}
