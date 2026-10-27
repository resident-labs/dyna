package voice

import (
	"encoding/binary"
	"testing"
)

func TestWAVHeaderMatchesSampleRateAndLength(t *testing.T) {
	pcm := []byte{1, 0, 2, 0, 3, 0}
	wav := WAVBytes(pcm, 24000)
	le := binary.LittleEndian
	if string(wav[:4]) != "RIFF" || string(wav[8:16]) != "WAVEfmt " || string(wav[36:40]) != "data" {
		t.Fatalf("bad chunk tags in %q", wav[:44])
	}
	if got := le.Uint32(wav[4:]); got != uint32(36+len(pcm)) {
		t.Errorf("RIFF size = %d, want %d", got, 36+len(pcm))
	}
	if le.Uint16(wav[20:]) != 1 || le.Uint16(wav[22:]) != 1 || le.Uint16(wav[34:]) != 16 {
		t.Errorf("want PCM, 1 channel, 16 bits")
	}
	if le.Uint32(wav[24:]) != 24000 || le.Uint32(wav[28:]) != 48000 || le.Uint16(wav[32:]) != 2 {
		t.Errorf("sample rate %d, byte rate %d, block align %d", le.Uint32(wav[24:]), le.Uint32(wav[28:]), le.Uint16(wav[32:]))
	}
	if le.Uint32(wav[40:]) != uint32(len(pcm)) || string(wav[44:]) != string(pcm) {
		t.Errorf("data chunk wrong: %v", wav[40:])
	}
}
