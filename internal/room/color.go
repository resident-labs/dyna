package room

import "fmt"

// RoomColor is one of the room identity colors.
type RoomColor string

const (
	ColorCrimson RoomColor = "crimson"
	ColorBlue    RoomColor = "blue"
	ColorViolet  RoomColor = "violet"
	ColorTeal    RoomColor = "teal"
	ColorOrange  RoomColor = "orange"
	ColorMagenta RoomColor = "magenta"
	ColorSky     RoomColor = "sky"
	ColorOlive   RoomColor = "olive"
)

var roomColors = [...]RoomColor{ColorCrimson, ColorBlue, ColorViolet, ColorTeal, ColorOrange, ColorMagenta, ColorSky, ColorOlive}

// ParseRoomColor validates a room color at an input boundary.
func ParseRoomColor(value string) (RoomColor, error) {
	for _, color := range roomColors {
		if string(color) == value {
			return color, nil
		}
	}
	return "", fmt.Errorf("unknown room color %q; choose crimson, blue, violet, teal, orange, magenta, sky or olive", value)
}

// nextRoomColor picks the first unused color, wrapping after all are used.
func nextRoomColor(records []Record) RoomColor {
	used := make(map[RoomColor]bool, len(records))
	for _, rec := range records {
		used[rec.Color] = true
	}
	for _, color := range roomColors {
		if !used[color] {
			return color
		}
	}
	return roomColors[len(records)%len(roomColors)]
}
