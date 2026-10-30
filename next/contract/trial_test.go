package contract

import (
	"encoding/json"
	"testing"
)

func TestImageFailureRoundTrip(t *testing.T) {
	event := Event{Body: ImageFailed{Host: "host", Repo: "owner/repo", Name: "image", Tag: "tag", Reason: "failed", LogTail: "log"}}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Event
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if body, ok := decoded.Body.(ImageFailed); !ok || body != event.Body.(ImageFailed) {
		t.Fatalf("unexpected image event: %+v", decoded.Body)
	}
}

func TestArchiveMessageRoundTrip(t *testing.T) {
	message, err := json.Marshal(BoxMessage{Body: Archive{}})
	if err != nil {
		t.Fatal(err)
	}
	var decodedMessage BoxMessage
	if err := json.Unmarshal(message, &decodedMessage); err != nil {
		t.Fatal(err)
	}
	if _, ok := decodedMessage.Body.(Archive); !ok {
		t.Fatalf("unexpected box message: %+v", decodedMessage.Body)
	}
}
