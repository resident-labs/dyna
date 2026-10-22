package roomsvc

import (
	"encoding/json"
	"testing"

	"dyna-agent/internal/transcript"
)

func TestSessionItemPreservesSteeringReceipt(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		item := sessionItem(transcript.Item{Kind: transcript.Steering, PostID: "msg_receipt", Delivered: delivered})
		data, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		var receipt struct {
			PostID    string `json:"postId"`
			Delivered bool   `json:"delivered"`
		}
		if err := json.Unmarshal(data, &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.PostID != "msg_receipt" || receipt.Delivered != delivered {
			t.Fatalf("receipt lost: %s", data)
		}
	}
}
