package rego_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/allenluce/panicmsg"
	"github.com/allenluce/rego"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("ReGo", func() {
	var r *rego.ReGo
	var count uint32
	BeforeEach(func() {
		r = rego.NewReGo()
		var c uint32
		atomic.StoreUint32(&count, c)
	})

	timedInc := func(countP *uint32, duration ...time.Duration) {
		if len(duration) > 0 {
			time.Sleep(duration[0])
		}
		atomic.AddUint32(countP, 1)
	}

	fPanic := func(msg interface{}, duration ...time.Duration) {
		if len(duration) > 0 {
			time.Sleep(duration[0])
		}
		panic(msg)
	}

	It("Takes a func", func() {
		r.Run(timedInc, &count)
		r.Wait()
		Ω(atomic.LoadUint32(&count)).Should(BeNumerically("==", 1))
	})
	It("Takes four funcs", func() {
		r.Run(timedInc, &count)
		r.Run(timedInc, &count)
		r.Run(timedInc, &count)
		r.Run(timedInc, &count)
		r.Wait()
		Ω(atomic.LoadUint32(&count)).Should(BeNumerically("==", 4))
	})
	It("raises panic immediately", func() {
		var wg sync.WaitGroup
		timedIncWaiter := func(countP *uint32, duration time.Duration) {
			defer wg.Done()
			timedInc(countP, duration)
		}
		r.Run(fPanic, "Things went hecka wrong")
		wg.Add(3)
		r.Run(timedIncWaiter, &count, time.Millisecond*100)
		r.Run(timedIncWaiter, &count, time.Millisecond*50)
		r.Run(timedIncWaiter, &count, time.Millisecond*5)
		Ω(r.Wait).Should(panicmsg.PanicMsg("Things went hecka wrong"))
		Ω(atomic.LoadUint32(&count)).Should(BeNumerically("==", 0))
		// Wait for goroutines to finish.
		wg.Wait()
	})
	It("will deal with random types of panics", func() {
		var ch chan string
		r.Run(fPanic, ch)
		Ω(r.Wait).Should(panicmsg.PanicMsg("(chan string)"))
		Ω(atomic.LoadUint32(&count)).Should(BeNumerically("==", 0))
	})
	It("nested regos unwind properly", func() {
		r.Run(func() {
			rego2 := rego.NewReGo()
			rego2.Run(func() {
				rego3 := rego.NewReGo()
				rego3.Run(fPanic, "Things went way wrong")
				rego3.Wait() // Will re-panic
			})
			rego2.Wait() // Will re-panic
		})
		Ω(r.Wait).Should(panicmsg.PanicMsg("Things went way wrong"))
	})
	It("stores and retrieves values", func() {
		vrego := rego.NewReGo(1000)
		Ω(vrego.Item()).Should(Equal(1000))
		vrego.SetItem("Another item")
		Ω(vrego.Item()).Should(Equal("Another item"))
	})
	It("will convert func errors to panics", func() {
		r.Run(errors.New, "An error")
		Ω(r.Wait).Should(panicmsg.PanicMsg("An error"))
	})
	It("will convert func errors to panics even when not first", func() {
		r.Run(func() (string, int, rune, error) {
			return "yup", 200, 'j', errors.New("An error")
		})
		Ω(r.Wait).Should(panicmsg.PanicMsg("An error"))
	})
	It("accept and run defers after everything else", func(done Done) {
		r.Run(timedInc, &count)
		r.Defer(func(numArg int) {
			Ω(numArg).Should(BeNumerically("==", 1234)) // Make sure args pass
			Ω(atomic.LoadUint32(&count)).Should(BeNumerically("==", 3))
			close(done)
		}, 1234)
		r.Run(timedInc, &count)
		r.Run(timedInc, &count)
		r.Wait()
	})
	It("defers run even if there are panics/errors in the pipeline", func() {
		r.Run(fPanic, "oops")
		r.Defer(timedInc, &count)
		Ω(r.Wait).Should(panicmsg.PanicMsg("oops"))
		Ω(atomic.LoadUint32(&count)).Should(BeNumerically("==", 1))
	})
	It("runs all defers first then reports defer panic", func() {
		r.Run(timedInc, &count)
		r.Defer(timedInc, &count)
		r.Defer(fPanic, "bad defer")
		r.Defer(timedInc, &count)
		Ω(r.Wait).Should(panicmsg.PanicMsg("bad defer"))
		Ω(atomic.LoadUint32(&count)).Should(BeNumerically("==", 3))
	})
	It("prioritizes main panics over deferred panics", func() {
		r.Defer(fPanic, "bad defer")
		r.Run(fPanic, "bad main")
		Ω(r.Wait).Should(panicmsg.PanicMsg("bad main"))
	})
	It("defers can just return an error instead", func() {
		r.Run(timedInc, &count)
		r.Defer(errors.New, "error in defer")
		Ω(r.Wait).Should(panicmsg.PanicMsg("error in defer"))
		Ω(atomic.LoadUint32(&count)).Should(BeNumerically("==", 1))
	})
	It("defers are run in fifo order", func() {
		var which []string
		var o sync.Mutex
		r.Run(timedInc, &count)
		r.Defer(func() {
			defer o.Unlock()
			o.Lock()
			which = append(which, "one")
		})
		r.Defer(func() {
			defer o.Unlock()
			o.Lock()
			which = append(which, "two")
		})
		r.Defer(func() {
			defer o.Unlock()
			o.Lock()
			which = append(which, "three")
		})
		r.Wait()
		Ω(which).Should(Equal([]string{"three", "two", "one"}))
		Ω(atomic.LoadUint32(&count)).Should(BeNumerically("==", 1))
	})
	It("will return from Wait when there's nothing to wait on", func(done Done) {
		r.Wait()
		close(done)
	})
	It("will run AfterAlls even when there's nothing to run after", func(done Done) {
		r.AfterAll(timedInc, &count)
		r.Wait()
		Ω(atomic.LoadUint32(&count)).Should(BeNumerically("==", 1))
		close(done)
	})
	It("accept and run AfterThese after current Runs", func(done Done) {
		var one, two, three bool
		var nOne, nTwo bool
		// These two first
		r.Run(func() {
			one = true
		})
		r.Run(func() {
			time.Sleep(time.Millisecond * 10)
			two = true
		})
		// Then this guy, guaranteed to be after the above two.
		r.AfterThese(func() {
			Ω(one && two).Should(BeTrue())
			nOne = true
		})
		r.Run(func() {
			three = true
		})
		r.AfterThese(func() {
			Ω(one && two && three).Should(BeTrue())
			nTwo = true
		})
		r.Defer(func() {
			Ω(one && two && three && nOne && nTwo).Should(BeTrue())
			close(done)
		})
		r.Wait()
	})
	It("properly deal with panics in AfterTheses", func(done Done) {
		r.Run(timedInc, &count, time.Millisecond*10)
		r.Run(timedInc, &count, time.Millisecond*20)
		r.AfterThese(fPanic, "Things went wrong")
		Ω(r.Wait).Should(panicmsg.PanicMsg("Things went wrong"))
		Ω(atomic.LoadUint32(&count)).Should(BeNumerically("==", 2))
		close(done)
	})
	It("allow AfterTheses to return errors", func(done Done) {
		r.Run(timedInc, &count, time.Millisecond*10)
		r.Run(timedInc, &count, time.Millisecond*20)
		r.AfterThese(func() error {
			time.Sleep(time.Millisecond * 10)
			return errors.New("Things went way wrong")
		})
		r.AfterThese(timedInc, &count) // Should not run!
		r.Defer(func() {
			defer GinkgoRecover()
			Ω(atomic.LoadUint32(&count)).Should(BeNumerically("==", 2))
			close(done)
		})
		Ω(r.Wait).Should(panicmsg.PanicMsg("Things went way wrong"))
	})
	Context("Full Pipeline Sequencing tests", func() {
		It("makes sure AfterThatOne/AfterThese/AfterAll don't run when Run panics", func(done Done) {
			var afterAll, afterThatOne, afterThese bool
			r.Defer(func() {
				Ω(afterAll || afterThatOne || afterThese).Should(BeFalse())
				close(done)
			})
			r.AfterAll(func() {
				afterAll = true
			})
			r.Run(fPanic, "Run panics", time.Millisecond*20) // Wait for others to be registered
			r.AfterThatOne(func() {
				afterThatOne = true
			})
			r.AfterThese(func() {
				afterThese = true
			})
			Ω(r.Wait).Should(panicmsg.PanicMsg("Run panics"))
		})
		It("makes sure AfterThatOne/AfterThese/AfterAll don't run when AfterThatOne panics", func(done Done) {
			var run, afterAll, afterThatOne2, afterThese bool
			r.Defer(func() {
				Ω(run).Should(BeTrue())
				Ω(afterAll || afterThese || afterThatOne2).Should(BeFalse())
				close(done)
			})
			r.AfterAll(func() {
				afterAll = true
			})
			r.Run(func() {
				run = true
			})
			r.AfterThatOne(fPanic, "AfterThatOne panics", time.Millisecond*20) // Wait for others to be registered
			r.AfterThatOne(func() {
				afterThatOne2 = true
			})
			r.AfterThese(func() {
				afterThese = true
			})
			Ω(r.Wait).Should(panicmsg.PanicMsg("AfterThatOne panics"))
		})
		It("makes sure AfterThatOne/AfterThese/AfterAll don't run when AfterThese panics", func(done Done) {
			var run, afterThatOne, afterThatOne2, afterThese2, afterAll bool
			r.Defer(func() {
				Ω(run && afterThatOne).Should(BeTrue())
				Ω(afterAll || afterThatOne2 || afterThese2).Should(BeFalse())
				close(done)
			})
			r.AfterAll(func() {
				afterAll = true
			})
			r.Run(func() {
				run = true
			})
			r.AfterThatOne(func() {
				afterThatOne = true
			})
			r.AfterThese(fPanic, "AfterThese panics", time.Millisecond*20) // Wait for others to be registered
			r.AfterThese(func() {
				afterThese2 = true
			})
			r.AfterThatOne(func() {
				afterThatOne2 = true
			})
			Ω(r.Wait).Should(panicmsg.PanicMsg("AfterThese panics"))
		})
		It("makes sure Defers run when AfterAll panics", func(done Done) {
			r.Defer(func() {
				close(done)
			})
			r.AfterAll(fPanic, "AfterAll panics")
			Ω(r.Wait).Should(panicmsg.PanicMsg("AfterAll panics"))
		})
	})
})

func TestRego(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Rego Suite")
}
