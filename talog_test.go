package tabc

import (
	"bytes"
	"io"
	"reflect"
	"testing"
	"time"

	"stash.cudaops.com/BNXENG/supporttunnel/securitylog"

	"github.com/davecgh/go-spew/spew"
	"github.com/thejerf/abtime"
)

func TestTALogSimple(t *testing.T) {
	now := time.Now()
	buf := CloseableBuffer()
	abt := abtime.NewManualAtTime(now)
	rl := &securitylog.RecordingLogger{}

	// Set up a recording logger based on the injected dependencies
	il := New(buf, abt, rl).(*inputLogger)

	for _, b := range []byte("hello world\n") {
		il.Write([]byte{b})
		il.sync()
		abt.Advance(time.Second)
	}
	il.sync()

	il.Close()

	expected := InputLoggerChunk{
		LoggedData: LoggedData{
			String:   "hello world⟪LF⟫ ",
			Bytes:    []byte("hello world\n"),
			BaseTime: now,
			DecisecondOffsets: []int{0, 10, 20, 30, 40, 50, 60, 70, 80, 90,
				100, 110},
		},
	}

	if !reflect.DeepEqual([]securitylog.LogMsg{expected}, rl.LogMsgs) {
		t.Fatal("talog doesn't seem to work at all")
	}
}

func TestTALogTimerFiring(t *testing.T) {
	now := time.Now()
	buf := CloseableBuffer()
	abt := abtime.NewManualAtTime(now)
	rl := &securitylog.RecordingLogger{}
	il := New(buf, abt, rl).(*inputLogger)

	// We have the user write "nonewline" with no newline, wait past the
	// timeout, which we fake, then hit enter.
	il.Write([]byte("nonewline"))
	il.testTrigger <- struct{}{}
	il.sync()
	abt.Advance(time.Second)
	il.Write([]byte("\n"))
	il.sync()

	expected := []securitylog.LogMsg{
		InputLoggerChunk{
			LoggedData{
				String:            "nonewline",
				Bytes:             []byte("nonewline"),
				BaseTime:          now,
				DecisecondOffsets: []int{0, 0, 0, 0, 0, 0, 0, 0, 0},
			},
		},
		InputLoggerChunk{
			LoggedData{
				String:            "⟪LF⟫ ",
				Bytes:             []byte("\n"),
				BaseTime:          now.Add(time.Second),
				DecisecondOffsets: []int{0},
			},
		},
	}
	if !reflect.DeepEqual(expected, rl.LogMsgs) {
		spew.Dump(rl.LogMsgs)
		t.Fatal("triggering time-aware log from timer doesn't seem to work")
	}
}

// Test when the user copies and pastes multiple lines
func TestTALogMultipleLines(t *testing.T) {
	now := time.Now()
	buf := CloseableBuffer()
	abt := abtime.NewManualAtTime(now)
	rl := &securitylog.RecordingLogger{}
	il := New(buf, abt, rl).(*inputLogger)

	il.Write([]byte("a\nb\nc\n"))
	il.sync()

	expected := []securitylog.LogMsg{
		InputLoggerChunk{
			LoggedData{
				String:            "a⟪LF⟫ ",
				Bytes:             []byte("a\n"),
				BaseTime:          now,
				DecisecondOffsets: []int{0, 0},
			},
		},
		InputLoggerChunk{
			LoggedData{
				String:            "b⟪LF⟫ ",
				Bytes:             []byte("b\n"),
				BaseTime:          now,
				DecisecondOffsets: []int{0, 0},
			},
		},
		InputLoggerChunk{
			LoggedData{
				String:            "c⟪LF⟫ ",
				Bytes:             []byte("c\n"),
				BaseTime:          now,
				DecisecondOffsets: []int{0, 0},
			},
		},
	}

	if !reflect.DeepEqual(expected, rl.LogMsgs) {
		spew.Dump(rl.LogMsgs)
		t.Fatal("multiple lines did not work as expected")
	}
}

func TestTALogNonUTF8(t *testing.T) {
	now := time.Now()
	buf := CloseableBuffer()
	abt := abtime.NewManualAtTime(now)
	rl := &securitylog.RecordingLogger{}
	il := New(buf, abt, rl).(*inputLogger)

	il.Write([]byte("テスト\n\xff\xff\x00\x00\nc\n"))
	il.sync()

	expected := []securitylog.LogMsg{
		InputLoggerChunk{
			LoggedData{
				String:            "テスト⟪LF⟫ ",
				Bytes:             []byte("テスト\n"),
				BaseTime:          now,
				DecisecondOffsets: []int{0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
			},
		},
		InputLoggerChunk{
			LoggedData{
				String:            "\xff\xff⟪NUL⟫ ⟪NUL⟫ ⟪LF⟫ ",
				Bytes:             []byte{255, 255, 0, 0, 10},
				BaseTime:          now,
				DecisecondOffsets: []int{0, 0, 0, 0, 0},
			},
		},
		InputLoggerChunk{
			LoggedData{
				String:            "c⟪LF⟫ ",
				Bytes:             []byte("c\n"),
				BaseTime:          now,
				DecisecondOffsets: []int{0, 0},
			},
		},
	}
	if !reflect.DeepEqual(expected, rl.LogMsgs) {
		spew.Dump(rl.LogMsgs)
		t.Fatal("Unicode or non-unicode handling incorrect")
	}
}

func TestCover(t *testing.T) {
	ilp := InputLoggerPanic{"aaa", "aaa"}
	ilp.Type()
	ilp.LogMsg()

	ilc := InputLoggerChunk{LoggedData{BaseTime: time.Now()}}
	ilc.Type()
	ilc.LogMsg()

	buf := CloseableBuffer()
	rl := &securitylog.RecordingLogger{}
	New(buf, nil, rl)

	timedInputToLoggedData(nil)
}

func CloseableBuffer() io.WriteCloser {
	return &closeableBuffer{bytes.NewBuffer([]byte{}), false}
}

type closeableBuffer struct {
	*bytes.Buffer
	closed bool
}

func (cb *closeableBuffer) Write(b []byte) (n int, err error) {
	if cb.closed {
		return 0, io.ErrClosedPipe
	}

	return cb.Buffer.Write(b)
}

func (cb *closeableBuffer) Close() error {
	cb.closed = true
	return nil
}
