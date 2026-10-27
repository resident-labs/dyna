package computers

import "testing"

// D-279: the compositor cannot screenshot a width that is not a multiple of 4.
func TestSizeAlignedRoundsWidthUpToAMultipleOfFour(t *testing.T) {
	for width, want := range map[int]int{388: 388, 389: 392, 390: 392, 392: 392, 393: 396, 7680: 7680} {
		got := Size{Width: width, Height: 845}.Aligned()
		if got.Width != want || got.Height != 845 {
			t.Errorf("Aligned(%dx845) = %dx%d, want %dx845", width, got.Width, got.Height, want)
		}
	}
}
