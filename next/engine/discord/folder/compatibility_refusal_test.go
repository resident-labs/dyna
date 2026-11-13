package folder

import "testing"

func TestUnsupportedChannelsStillRefuseUnsafeStructure(t *testing.T) {
	for _, channels := range [][]liveChannel{
		{{ID: "1", Name: "duplicate", ParentID: "200", Type: 13}, {ID: "2", Name: "duplicate", ParentID: "200", Type: 13}},
		{{ID: "1", Name: "../unsafe", Type: 13}},
		{{ID: "1", Name: "nested", Type: 4, ParentID: "200", Flags: 16}},
	} {
		if _, err := snapshotChannels(Snapshot{}, channels, nil, map[string]string{"200": "Community"}, nil); err == nil {
			t.Fatalf("accepted unsafe structure: %+v", channels)
		}
	}
}
