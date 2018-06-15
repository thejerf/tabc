/*

Package tabc implement time-aware bytestream consolidation - given an
incoming stream of []byte over time, it assembles the chunks into a
more meaningful collection of []byte chunks.

This was originally written to take a stream of keystrokes over a terminal
session in realtime, and assemble them into more meaningful chunks for
logging purposes, but may be useful for other similar uses.

Note this should NOT be used for network packet re-assembly. Use
io.ReadFull or something similar.

Use With Standard Interfaces

This package provides io.Writer- and io.Reader-compatible wrapper objects
for your convenience. The io.Reader semantics are basically unchanged
from normal io.Reader semantics. However, because the entire purpose
of this package is to temporally detach the input of bytes and the
output of them, the semantics of io.Writer are more complicated,
because there may be a time delay before the underlying writer is
written to. If that write fails, it will fail at a time where it can
not simply return an error. Therefore, the error may manifest either
on later .Write calls, or possibly at .Close time, when the output
is flushed all the way through. For similar reasons, the consolidator
will return that bytes have been written before they have actually
been written, so simply adding together the number bytes returned
by the consolidator's .Write function may overestimate the number of bytes
actually written.

Consequently, it is particularly important to check the errors, even
from .Close, when using a Consolidator, as you may otherwise miss
write errors.

*/
package tabc

import (
	"fmt"
	"io"
	"log"
	"runtime/debug"
	"time"
)

const (
	// FIXME: parameters

	// DefaultMaxWaitInterval defines the default longest amount of time a
	// Consolidator will wait before emitting its current input as output.
	DefaultMaxWaitInterval = time.Second * 5
)

// Options defines the options for the Consolidator, and can be passed to
// NewWriterOpt or NewReaderOpt.
//
// MaxBlockSize defines the largest message that will be emitted as a
// single block. If set to zero, the consolidator will not take block size
// into account for any decisions. // FIXME: Ensure this works
//
// Partitioner is a function that can examine the current input of the
// consolidated logger, and emit messages early. For instance, this can be
// used to emit based on newlines. Partitioners are NOT allowed to modify
// the []byte passed in to them. By default the only partitioning done will
// be based on the MaxBlockSize. newByteIdx indicates to your function the
// first byte that is new for this invocation of the partitioner. Most
// partitioner functions should only start their examination there, to
// avoid turning into accidental O(n^2) functions as bytes come in.
//
// If you have neither a MaxBlockSize nor a Partitioner, you probably don't
// want a Consolidator at all; the effect of such a consolidator would be
// to gather all input in memory until the end of the stream, then dump it
// all out at once, in which case you should just use io.ReadFull or a
// bytes.Buffer, as appropriate.
//
// WaitInterval defines how long the consolidator will wait for more input,
// before emitting what it has. If left zero, this will be set to the
// DefaultMaxWaitInterval constant.
//
// Logger accepts a function that will take a string coming from the
// consolidator, and log it. If left nil, this will be set to use the
// default Go logger package.
//
// TimeShim allows you to override the mechanism used to obtain the current
// time, and timers. If left nil, the consolidator will use the core time
// package as you'd expect.
type Options struct {
	MaxBlockSize int
	Partitioner  func(input []byte, newByteIdx) []int
	WaitInterval time.Duration
	Logger       func(string)
	TimeShim
}

func (o *Options) setDefaults() {
	if o.WaitInterval == 0 {
		o.WaitInterval = DefaultMaxWaitInterval
	}

	if o.Logger == nil {
		o.Logger = func(s string) {
			log.Print(s)
		}
	}

	if o.TimeShim == nil {
		o.TimeShim = corelibTimeShim{}
	}
}

// Consolidator is the core type used to consolidate incoming byte
// streams. Additional wrappers are provided to implement io.Writer and
// io.Reader interfaces, but these will by necessity drop the offset times
// for the bytes. If you need those, you will need to directly use a
// Consolidator.
//
// All legal methods for obtaining a Consolidator will also start a
// goroutine. All consolidators must be closed when you are done with them,
// or you will leak a goroutine.
type consolidator struct {
	Options

	// outputFunc is what the consolidator uses to finally output the bytes.
	outputFunc func([]byte, time.Time, []time.Duration) error

	inputC     chan inputMsg
	closed     bool
	errorState error

	// used internally for testing, allowing us to sync on the reading
	// being done
	syncC chan struct{}

	// This information is private to the running goroutine, and represents
	// the state we are currently in.

	// Do we currently have input? If false, all the metadata about
	// the current state of the keystrokes is irrelevant.
	haveInput bool
	// The currently-tracked input
	input []timedInput
	// the timer, if any, that is currently active
	timer Timer

	flushTrigger chan chan error

	// What to close, if anything.
	underlyingCloser io.Closer
}

type consolidatorWriter struct {
	consolidator
}

// WriteCloseFlusher is a WriteCloser that can also be Flush()ed, which
// forces all the output immediately.
type WriteCloseFlusher interface {
	io.WriteCloser
	Flush() error
}

// New wraps the given io.WriteCloser with the code to do time-aware
// logging of what is written to the given securitylog.Session.
//
// While this function only requires an io.Writer, if the passed-in
// parameter also conforms to io.Closer it will be .Closed when .Close is
// called on the returned io.WriteCloser.
func NewWriter(wc io.Writer) io.WriteCloser {
	c := &consolidatorWriter{consolidator{
		inputC:       make(chan inputMsg),
		syncC:        make(chan struct{}),
		flushTrigger: make(chan chan error),
	}}
	c.Options.setDefaults()

	closer, _ := wc.(io.Closer)
	c.consolidator.underlyingCloser = closer

	go c.consolidator.run()

	return c
}

func (cw *consolidatorWriter) Write(b []byte) (int, error) {
	err := cw.consolidator.addInput(b)
	if err == nil {
		return len(b), nil
	}
	return 0, err
}

func (cw *consolidatorWriter) Flush() error {
	errC := make(chan error)
	cw.flushTrigger <- errC
	return <-errC
}

type consolidatorReader struct {
	consolidator
}

type inputMsg struct {
	input []byte
	reply chan error
}

func (c *consolidator) Close() error {
	if !c.closed {
		c.closed = true
		close(c.inputC)
	}

	if c.underlyingCloser != nil {
		return c.underlyingCloser.Close()
	}
	return nil
}

// addInput is used from the external goroutine to add input to the
// consolidator.
//
// The returned error may be from earlier attempts to add input.
func (c *consolidator) addInput(b []byte) error {
	// We have to make a copy, because we're going to return before we're
	// "done" with these bytes.
	cpy := make([]byte, len(b))
	copy(cpy, b)
	msg := inputMsg{cpy, make(chan error)}
	c.inputC <- msg
	return <-msg.reply
}

func (c *consolidator) run() {
	defer func() {
		r := recover()
		if r != nil {
			stack := string(debug.Stack())
			panicMsg := fmt.Sprintf("panic in tabc.consolidator: %v\nat: %s",
				r, stack)
			c.Logger(panicMsg)
		}
	}()

	for {
		var timerC <-chan time.Time
		if c.timer == nil {
			timerC = nil
		} else {
			timerC = c.timer.Channel()
		}

		select {
		case nextInput, ok := <-c.inputC:
			if !ok {
				// We seem to be done here. Log anything remaining and
				// terminate this goroutine.
				c.doLog()
				return
			}

			c.handleInput(nextInput.input)

		case reply := <-c.flushTrigger:
			reply <- c.doLog()

		case <-timerC:
			c.timer = nil
			c.doLog()

		// This allows us to synchronize on this loop, so the tests can
		// safely know the previous requests were processed.
		case <-c.syncC:
		}
	}
}

// sync is used in the testing to wait on the consolidator being done with
// its tasks before we examine its internal state.
func (c *consolidator) sync() {
	c.syncC <- struct{}{}
}

// handleInput is used within the consolidator's goroutine to handle
// incoming input.
func (c *consolidator) handleInput(nextInput []byte) {
	// we want to do this last, regardless of what is going on.
	// terminate the current timer, as it is out-of-date now.
	// If there is any input still queued up to go out, start a new timer,
	// but if it's empty, don't start a new timer.
	defer func() {
		// see the time pkg documentation on what this is in the Timer
		// section, and why we don't use
		if c.timer != nil && !c.timer.Stop() {
			<-c.timer.Channel()
		}
		if len(c.input) > 0 {
			c.timer.Reset(c.Options.WaitInterval)
		} else {
			c.input = nil
		}
	}()

	now := c.TimeShim.Now()

	input = append(input, nextInput...)

	if c.Options.Partitioner != nil {
		partitions := c.Options.Partitioner(...)
	}
	beginIdx := 0
	for idx, b := range nextInput {
		
		// First condition: Is this a newline?
		if b == 10 || b == 13 {
			// we have a newline!
			// make a new timedInput with the rest of the line
			lineTimedInput := timedInput{
				input: nextInput[beginIdx : idx+1],
				time:  now,
			}

			c.input = append(c.input, lineTimedInput)

			c.doLog()
			beginIdx = idx + 1
		}

		// Second condition: Would this push us over our limit for a single
		// log message?
		// FIXME
	}

	// Whatever's left over, turn it into a timedInput and append to the
	// current list of timed inputs. In the common case, none of the above
	// triggers, so this just becomes "append this keystroke to the list of
	// inputs".
	if beginIdx < len(nextInput) {
		c.input = append(c.input,
			timedInput{
				input: nextInput[beginIdx:],
				time:  now,
			})
	}
}

// doLog will take the given timedInput and write it out to the log.
func (c *consolidator) doLog() error {
	// FIXME: Honor block size here
	output := c.input
	err := c.outputFunc(output)
	c.input = c.input[:0]
	return err
}
