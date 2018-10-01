/*

Package tabc implement time-aware bytestream consolidation - given an
incoming stream of []byte over time, it assembles the chunks into a
more meaningful collection of []byte chunks.

This was originally written to take a stream of keystrokes over a terminal
session in realtime, and assemble them into more meaningful chunks for
logging purposes, but may be useful for other similar uses.

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
// Partitioner is a function that is given the internal state of the
// current input, and can call the given output function as well as return
// the new internal state of the consolidator. This can be used to do
// things like break the input up according to your own constraints,
// etc. See the examples in (SHOW).
//
// WaitInterval defines how long the consolidator will wait for more input,
// before emitting what it has. If left zero, this will be set to the
// DefaultMaxWaitInterval constant.
//
// Logger accepts a function that will take a string coming from the
// consolidator, and log it. If left nil, this will be set to use the
// default Go logger package.
//
// OnFlush allows you to specify a function that will be called upon
// successfully Flush()ing the Consolidator. This allows you to do things
// like .Flush() or .Sync() the underlying .Writer if you need to. OnFlush
// code should be aware that the underlying writer may be in an error
// state.
//
// TimeShim allows you to override the mechanism used to obtain the current
// time, and timers. If left nil, the consolidator will use the core time
// package as you'd expect.
type Options struct {
	OnFlush      func() error
	WaitInterval time.Duration
	Logger       func(string)
	TimeShim
}

type Partitioner func([]timedInput, func(Consolidated) error) ([]timedInput, error)

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
	outputFunc func(Consolidated) error

	Partitioner Partitioner
	onFlush     func() error

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

	underlying io.Writer
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

// New wraps the given io.Writer with the code to do time-aware
// logging of what is written to the given securitylog.Session.
//
// While this function only requires an io.Writer, if the passed-in
// parameter also conforms to io.Closer it will be .Closed when .Close is
// called on the returned io.WriteCloser.
func NewWriter(wc io.Writer, partitioner Partitioner) io.WriteCloser {
	c := &consolidatorWriter{consolidator{
		inputC:       make(chan inputMsg),
		syncC:        make(chan struct{}),
		flushTrigger: make(chan chan error),
		Partitioner:  partitioner,
	}}
	c.Options.setDefaults()

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

	closer, isCloser := c.underlying.(io.Closer)
	if isCloser {
		return closer.Close()
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
			err := c.doLog()
			if err != nil {
				reply <- err
				continue
			}
			if c.onFlush != nil {
				err = c.onFlush()
				reply <- err
			} else {
				reply <- nil
			}

		case <-timerC:
			c.timer = nil
			err := c.doLog()
			if err != nil {
				c.errorState = err
			}

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
// incoming input. Note this does not return an error; that is because when
// this is executed, there's basically nowhere to send it.
func (c *consolidator) handleInput(nextInput []byte) {
	if c.errorState != nil {
		return
	}

	// we want to do this last, regardless of what is going on.
	// terminate the current timer, as it is out-of-date now.
	// If there is any input still queued up to go out, start a new timer,
	// but if it's empty, don't start a new timer.
	defer func() {
		// see the time pkg documentation on what this is in the Timer
		// section
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
	c.input = append(c.input, timedInput{nextInput, now})

	newState, err := c.Partitioner(c.input, c.outputFunc)
	if err != nil {
		c.errorState = err
		return
	}
	c.input = newState
}

// doLog will take the given timedInput and write it out to the log.
func (c *consolidator) doLog() error {
	if c.errorState != nil {
		return c.errorState
	}

	output, haveOutput := timedInputToLoggedData(c.input)
	if haveOutput {
		err := c.outputFunc(output)
		c.input = c.input[:0]
		c.errorState = err
		return err
	}
	return nil
}
