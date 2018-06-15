package talog

import "fmt"

type InputLoggerPanic struct {
	StackTrace string      `json:"stacktrace"`
	PanicVal   interface{} `json:"panicval"`
}

func (ilp InputLoggerPanic) LogMsg() string {
	return fmt.Sprintf("Input logger paniced with %#v; stacktrace: %s",
		ilp.PanicVal, ilp.StackTrace)
}

func (ilp InputLoggerPanic) Type() string {
	return "input_logger_panic"
}

// InputLoggerChunk represents a logged chunk of data.
//
// This may just end up collapsed into the LoggedData value here, we'll see.
type InputLoggerChunk struct {
	LoggedData
}

func (ilc InputLoggerChunk) LogMsg() string {
	return fmt.Sprintf("Input logged at %s: %q (decisecond offsets: %v)",
		ilc.LoggedData.BaseTime.String(),
		ilc.LoggedData.String,
		ilc.LoggedData.DecisecondOffsets,
	)
}

func (ilc InputLoggerChunk) Type() string {
	return "input_logger_chunk"
}
