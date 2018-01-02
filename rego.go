package rego

import (
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ReGo fires off goroutines and waits until they are all done or one
// panics (or returns an error). If one panics/errors, a panic is raised
// on thread that calls Wait().
//
// ReGo provides several synchronization mechanisms to order the
// execution of goroutines.
type ReGo struct {
	curWg, lastWg      *sync.WaitGroup
	afterAllCond       *sync.Cond
	status             chan error
	howmany, afterAlls uint32
	afterAllRunning    int
	item               interface{}
	defers             [][]interface{}
}

// NewReGo returns a ReGo object with an initialized status channel.
func NewReGo(item ...interface{}) *ReGo {
	r := &ReGo{
		status:       make(chan error, 1000),
		curWg:        &sync.WaitGroup{},
		afterAllCond: sync.NewCond(&sync.Mutex{}),
	}
	if len(item) > 0 {
		r.item = item[0]
	}
	return r
}

// Item gives an optional item that this ReGo encapsulates.
func (rego *ReGo) Item() interface{} {
	return rego.item
}

// SetItem sets the optional item that this ReGo encapsulates.
func (rego *ReGo) SetItem(item interface{}) {
	rego.item = item
}

type reGoPanic struct { // For labeling internal panics
	error
}

// drainRoutines checks the status of running routines and signals the
// AfterAlls to run once the rest are done. It then passes control to
// checkDefers().
func (rego *ReGo) drainRoutines() {
	var mainPanic interface{}
	for e := range rego.status {
		if e != nil {
			mainPanic = e
			break
		}
		howMany := atomic.AddUint32(&rego.howmany, ^uint32(0))
		if howMany == 0 {
			break
		}
		afterAlls := atomic.LoadUint32(&rego.afterAlls)
		if howMany-afterAlls == 0 { // Others are done, run afteralls.
			rego.afterAllCond.Broadcast()
		}
	}
	rego.checkDefers(mainPanic)
}

// Wait waits for all goroutines to finish or panic. If one panics any
// Defer()s are run then the original panic is re-raised (in the
// thread calling Wait()) and Wait() returns immediately without
// waiting for the other (non-Defer) routines to finish.
//
// NOTE TO UNIT TEST IMPLEMENTORS:
// Code that uses ReGo and expects panics (a common unit test
// scenario) may not get one if Wait() is not called.  Calling Wait() in a defer
// should guarantee that errors/panics in a goroutine managed by ReGo
// are seen and converted to errors/panics on the calling thread.  This
// pattern can keep tests from hanging.
func (rego *ReGo) Wait() {
	// Don't eat panics if we're called within a defer.
	if r := recover(); r != nil {
		panic(r)
	}

	howMany := atomic.LoadUint32(&rego.howmany)
	if howMany == 0 && len(rego.status) == 0 {
		return // Nothing to wait on!
	}

	afterAlls := atomic.LoadUint32(&rego.afterAlls)
	// Make sure all AfterAlls are waiting on the cond
	for {
		rego.afterAllCond.L.Lock()
		if rego.afterAllRunning == int(afterAlls) {
			rego.afterAllCond.L.Unlock()
			break
		}
		rego.afterAllCond.L.Unlock()
		time.Sleep(time.Millisecond) // To prevent spinning
	}

	if howMany-afterAlls == 0 { // Others are done, run afteralls.
		rego.afterAllCond.Broadcast()
	}
	rego.drainRoutines()
}

func (rego *ReGo) checkDefers(mainPanic interface{}) {
	deferStatus := make(chan error, len(rego.defers))
	// Run any deferred routines in FIFO order
	for i := len(rego.defers) - 1; i >= 0; i-- {
		deferred := rego.defers[i]
		f := reflect.ValueOf(deferred[0])
		rargs := make([]reflect.Value, len(deferred[1:]))
		for i, a := range deferred[1:] {
			rargs[i] = reflect.ValueOf(a)
		}
		func() { // Do defers serially
			defer rego.checkPanics(deferStatus)
			errs := f.Call(rargs)
			for _, err := range errs {
				if e, ok := err.Interface().(error); ok { // But it returned an error!
					panic(reGoPanic{
						fmt.Errorf("%s at %s", e.Error(), identifyPanic()),
					})
				}
			}
		}()
	}
	close(deferStatus)
	// Panic from the non-afterAlls takes priority
	if mainPanic != nil {
		panic(mainPanic)
	}
	// Then any deferred panics
	for e := range deferStatus {
		if e != nil {
			panic(e)
		}
	}
}

func identifyPanic() string {
	var pc [16]uintptr

	n := runtime.Callers(3, pc[:])
	frames := runtime.CallersFrames(pc[:n])
	var frame runtime.Frame
	more := true
	var stack string
	for more { // Remove us and the panic runtime from the top of the stack.
		frame, more = frames.Next()
		if strings.Contains(frame.Function, "checkPanics") ||
			strings.HasPrefix(frame.Function, "runtime.") {
			continue
		}
		break
	}
	stack += fmt.Sprintf("    %s()\n        %s:%d\n", frame.Function, frame.File, frame.Line)
	for more {
		frame, more = frames.Next()
		stack += fmt.Sprintf("    %s()\n        %s:%d\n", frame.Function, frame.File, frame.Line)
	}
	return stack
}

func regoPanic(msg string) error { // Helper wrapper
	return &reGoPanic{
		fmt.Errorf("%s at:\n%s", msg, identifyPanic()),
	}
}

// checkPanics is run in a defer statement and puts any panics onto
// the status channel. If there are no panics, it puts a nil value
// (meaning success) onto the channel.  If there are no panics, it
// calls Done on any given wgs so the chain can continue.
func (rego *ReGo) checkPanics(status chan error, wgs ...*sync.WaitGroup) {
	var msg error
	if r := recover(); r != nil {
		switch e := r.(type) { // Panic! Signal the error.
		case reGoPanic:
			msg = &e
		case error:
			msg = regoPanic(e.Error())
		case string:
			msg = regoPanic(e)
		default:
			msg = regoPanic(fmt.Sprintf("%#v", r))
		}
	}
	select {
	case status <- msg:
	default:
		panic("TOO MANY REGOS")
	}
	if msg == nil {
		for _, wg := range wgs {
			wg.Done()
		}
	}
}

func (rego *ReGo) panicOnError(errs []reflect.Value) {
	for _, err := range errs {
		switch e := err.Interface().(type) {
		case error:
			panic(regoPanic(e.Error()))
		case []reflect.Value: // Unwrap
			rego.panicOnError(e)
		}
	}
}

// Use reflection to call fn with args
func call(fn interface{}, args []interface{}) []reflect.Value {
	f := reflect.ValueOf(fn)
	funcType := reflect.TypeOf(fn)
	rargs := make([]reflect.Value, len(args))
	for i, a := range args {
		if a == nil { // Handle nil values
			expectedType := funcType.In(i)
			rargs[i] = reflect.New(expectedType).Elem()
		} else {
			rargs[i] = reflect.ValueOf(a)
		}
	}
	return f.Call(rargs)
}

// Run runs the given function as a goroutine with the supplied
// arguments. It returns immediately.
//
// After calling Run() with the functions to run, Wait() must be
// called to wait on those functions to finish. If one of the
// functions panics, Wait() will also panic with the message from the
// original panicking function.
//
// The function is expected to either panic or return an error should
// anything go wrong. The error can be returned in any position.
func (rego *ReGo) Run(fn interface{}, args ...interface{}) {
	atomic.AddUint32(&rego.howmany, 1)
	rego.lastWg = &sync.WaitGroup{}
	rego.lastWg.Add(1)
	rego.curWg.Add(1)
	go func(wg, lastWg *sync.WaitGroup) {
		defer rego.checkPanics(rego.status, wg, lastWg)
		rego.panicOnError(call(fn, args))
	}(rego.curWg, rego.lastWg)
}

// AfterThatOne calls Run() on the given function and args but only
// after the immediately prior Run/AfterThatOne/AfterThese function
// has finished with no error or panics.
func (rego *ReGo) AfterThatOne(fn interface{}, args ...interface{}) {
	rego.Run(func(wg *sync.WaitGroup) []reflect.Value {
		wg.Wait()
		return call(fn, args)
	}, rego.lastWg)
}

// AfterThese calls Run() on the given function and args after all the
// prior register Run/AfterThatOne/AfterThese functions have finished
// with no error or panics.
func (rego *ReGo) AfterThese(fn interface{}, args ...interface{}) {
	oldWg := rego.curWg
	rego.curWg = &sync.WaitGroup{}
	rego.Run(func() []reflect.Value {
		oldWg.Wait()
		return call(fn, args)
	})
}

// AfterAll schedules a function to run after all other current and
// future registered functions have completed without panic or error.
// The function given to AfterAll is not counted as one that
// AfterThatOne or AfterThese will run after.
func (rego *ReGo) AfterAll(fn interface{}, args ...interface{}) {
	atomic.AddUint32(&rego.afterAlls, 1)
	atomic.AddUint32(&rego.howmany, 1)
	go func() {
		defer rego.checkPanics(rego.status)
		rego.afterAllCond.L.Lock()
		rego.afterAllRunning++
		rego.afterAllCond.Wait()
		rego.afterAllCond.L.Unlock()
		rego.panicOnError(call(fn, args))
	}()
}

// Defer runs the given function after all other registered functions
// are done or after one of them errors or panics.
func (rego *ReGo) Defer(fn interface{}, args ...interface{}) {
	entry := []interface{}{fn}
	entry = append(entry, args...)
	rego.defers = append(rego.defers, entry)
}
