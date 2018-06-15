package tabc

import "time"

// TimeShim allows you to define an alternate mechanism for obtaining the
// current time and timers. This allows you to test code that uses the
// consolidator.
//
// As it happens, a github.com/thejerf/abtime.AbstractTime value will
// conform to this interface, but you should be to hook it up to any other
// time simulator with just a bit of glue code.
type TimeShim interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

// Timer abstracts the interface of the value returned by time.NewTimer, so
// it can be replaced. See the TimeShim interface.
type Timer interface {
	Stop() bool
	Channel() <-chan time.Time
	Reset(time.Duration) bool
}

type corelibTimeShim struct{}

func (cts corelibTimeShim) Now() time.Time {
	return time.Now()
}

func (ctl corelibTimeShim) NewTimer(d time.Duration) Timer {
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

func (tw timerwrap) Reset(d time.Duration) bool {
	return tw.timer.Reset(d)
}
