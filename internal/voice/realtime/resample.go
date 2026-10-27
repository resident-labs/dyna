package realtime

import "encoding/binary"

// PageRate is the sample rate, in hertz, of the PCM16 mono audio the
// owner's page sends and plays (web/src/lib/voice/wire.js RATE).
const PageRate = 24000

// resample converts PCM16 little-endian mono audio from rate from to rate
// to by linear interpolation. Equal rates return pcm unchanged.
func resample(pcm []byte, from, to int) []byte {
	if from == to || from <= 0 || to <= 0 {
		return pcm
	}
	in := len(pcm) / 2
	if in == 0 {
		return nil
	}
	sample := func(i int) float64 { return float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) }
	n := in * to / from
	out := make([]byte, 2*n)
	for j := range n {
		pos := float64(j) * float64(from) / float64(to)
		i := int(pos)
		v := sample(i)
		if i+1 < in {
			v += (sample(i+1) - v) * (pos - float64(i))
		}
		binary.LittleEndian.PutUint16(out[2*j:], uint16(int16(v)))
	}
	return out
}

// Resample converts PCM16 little-endian mono audio from rate from to rate to.
func Resample(pcm []byte, from, to int) []byte { return resample(pcm, from, to) }
