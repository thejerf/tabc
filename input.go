package tabc

import (
	"time"
)

// FIXME: consolidate this file?

type timedInput struct {
	input []byte
	time  time.Time
}

// Consolidated is what comes out from a Consolidator. It has the full
// stream of bytes received (no bytes will be elided), the start time of
// this chunk of bytes, and individual offsets from that time for each
// byte, indexed by the byte. The first offset will always be 0, because
// the first byte is also guaranteed to be the base time.
type Consolidated struct {
	Bytes    []byte
	BaseTime time.Time
	Offsets  []time.Duration
}

// timedInputToLoggedData returns the converted LoggedData for the given
// list of timedInputs. If there are no inputs, this will return false on
// the second value, to indicate that there was no input to convert so the
// LoggedData is not valid.
//
// The parameter passed to this is allowed to be nil, but there may not be
// any nils in the slice passed in.
func timedInputToLoggedData(inputs []timedInput) (Consolidated, bool) {
	if len(inputs) == 0 {
		return Consolidated{}, false
	}

	l := 0
	for _, ti := range inputs {
		l += len(ti.input)
	}
	b := make([]byte, 0, l)
	offsets := make([]time.Duration, 0, l)

	baseTime := inputs[0].time

	for _, input := range inputs {
		for _, byte := range input.input {
			b = append(b, byte)
			offsets = append(offsets, input.time.Sub(baseTime))
		}
	}

	ld := Consolidated{
		Bytes:    b,
		BaseTime: baseTime,
		Offsets:  offsets,
	}

	return ld, true
}
