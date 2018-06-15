/*

Package tabc implement time-aware bytestream consolidation - given an
incoming stream of []byte over time, it assembles the chunks into a
more meaningful collection of []byte chunks.

For example, this can be used to take a stream of keystrokes in, each
coming as an individual `.Write(...)` call, and convert them into lines of
input.

*/
package tabc

import (
	"fmt"
	"io"
	"runtime/debug"
	"time"
)

const (
	// FIXME: parameters

	// MaxWaitInterval is the amount of time we will wait after what
	// appears to be an incomplete line before we log it and clear the
	// state.
	MaxWaitInterval = time.Second * 5

	// The maximum amount of bytes we'll send in one log message.
	MaxBlockSize = 16384
)

type ConsolidatorWriter struct {
	Consolidator
}

type ConsolidatorReader struct {
	Consolidator
}

// Params allows you to perform advanced configuration of the
// consolidators. See the Consolidator type for the meaning of the parameters.
type Params struct {
	Time
}

// Consolidator is the core type used to consolidate incoming byte
// streams. Additional wrappers are provided to implement io.Writer and
// io.Reader interfaces, but these will by necessity drop the offset times
// for the bytes. If you need those, you will need to directly use a Consolidator.
type Consolidator struct {
	inputC chan []byte
	closed bool
	// while this is open, the listener is operating. If it fails, this
	// will close and the writer can handle the error somehow.
	//
	// FIXME: If the logging fails, do we fail open or closed?
	liveC chan struct{}
	// used internally for testing, allowing us to sync on the reading
	// being done
	syncC chan struct{}

	waitInterval time.Duration
	maxBlockSize int

	// This information is private to the running goroutine, and represents
	// the state we are currently in.

	// Do we currently have input? If false, all the metadata about
	// the current state of the keystrokes is irrelevant.
	haveInput bool
	// The currently-tracked input
	input []timedInput
	// the timer, if any, that is currently active
	timer       Timer
	testTrigger chan struct{}
}

// New wraps the given io.WriteCloser with the code to do time-aware
// logging of what is written to the given securitylog.Session.
func NewWriter(wc io.WriteCloser) io.WriteCloser {
	c := &Consolidator{
		inputC:      make(chan []byte),
		liveC:       make(chan struct{}),
		syncC:       make(chan struct{}),
		testTrigger: make(chan struct{}),
		time:        time,
		underlying:  wc,
	}

	go c.Run()

	return c
}

func (c *Consolidator) Close() error {
	if !c.closed {
		c.closed = true
		close(c.inputC)
	}

	return c.underlying.Close()
}

func (c *Consolidator) Write(b []byte) (int, error) {
	// this has the effect of either sending the input to the auxiliary
	// goroutine, or if it has crashed and closed the liveC, ignoring the
	// send and passing through, at which point the underlying write will
	// occur. This implements failing open, if the logger fails.
	//
	// However, if the Run() method wedges, this will wedge too, and the
	// underlying writes will fail and the shell session will freeze.
	cpy := make([]byte, len(b))
	copy(cpy, b)
	select {
	case c.inputC <- cpy:
	case _, _ = <-c.liveC:
	}

	return c.underlying.Write(b)
}

func (c *Consolidator) Run() {
	defer func() {
		r := recover()
		if r != nil {
			stack := string(debug.Stack())
			c.syslog.Notice(InputLoggerPanic{
				StackTrace: string(stack),
				PanicVal:   r,
			})
			close(c.liveC)
			panic(r)
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

			c.handleInput(nextInput)

		case <-c.testTrigger:
			c.doLog()
		case <-timerC:
			fmt.Println("Chan fired:", timerC)
			c.timer = nil
			c.doLog()

		// This allows us to synchronize on this loop, so the tests can
		// safely know the previous requests were processed.
		case <-c.syncC:
		}
	}
}

func (c *Consolidator) sync() {
	c.syncC <- struct{}{}
}

// handleInput is the payload function that handles the next input.
func (c *Consolidator) handleInput(nextInput []byte) {
	// we want to do this last, regardless of what is going on.
	// terminate the current timer, as it is out-of-date now.
	// If there is any input still queued up to go out, start a new timer,
	// but if it's empty, don't start a new timer.
	defer func() {
		// see the time pkg documentation on what this is in the Timer
		// section
		if c.timer != nil && !c.timer.Stop() {
			fmt.Println("Waiting to drain", c.timer.Channel())
			<-c.timer.Channel()
		}
		c.timer = nil

		if len(c.input) > 0 {
			c.timer = c.time.NewTimer(MaxWaitInterval, timerID)
		}
	}()

	now := c.time.Now()

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
func (c *Consolidator) doLog() {
	loggedData, isValid := timedInputToLoggedData(c.input)
	if !isValid {
		return
	}

	c.input = c.input[:0]
	chunk := InputLoggerChunk{LoggedData: loggedData}
	c.syslog.Notice(chunk)
}

// Timer abstracts the time.Timer this package uses, allowing you to
// replace it for testing or other purposes.
type Timer interface {
	Channel() <-chan time.Time
	Stop() bool
}

// Time abstracts the interface to the time package this code
// uses. Modifying the .Time element of a Consolidator allows you to
// override this handling.
type Time interface {
	Now() time.Time
	Timer(time.Duration) Timer
}

type normalTime struct{}

func (nt normalTime) Now() time.Time {
	return time.Now()
}

func (nt normalTime) Timer(d time.Duration) Timer {
	return timerwrap{time.NewTimer(d)}
}

type timerwrap struct {
	timer *time.Timer
}

func (tw timerwrap) Channel() <-chan time.Time {
	return tw.timer.C
}

func (tw timerwrap) Stop() bool {
	return tw.timer.Stop()
}
