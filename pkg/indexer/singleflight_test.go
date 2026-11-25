package indexer

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// What singleflight is for, stated as a count.
//
// The fan-out it collapses is the same fan-out that earns the per-IP 429s:
// every merged page asks every network, every realm page asks several
// questions, and a crawler walking the same page ten times used to be ten
// upstream requests for one answer. Nothing here was cached except typeSupport,
// which is schema capability rather than data.

// blockingIndexer holds every request open until it is released, so several
// callers are provably in flight at once, and counts the round trips.
type blockingIndexer struct {
	hits    int64
	arrived chan struct{}
	release chan struct{}
	answer  func(query string) (status int, body string)
}

func (b *blockingIndexer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	atomic.AddInt64(&b.hits, 1)
	select {
	case b.arrived <- struct{}{}:
	default:
	}
	<-b.release
	status, body := b.answer(string(raw))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func newBlockingIndexer(t *testing.T, answer func(query string) (int, string)) (*blockingIndexer, *httptest.Server) {
	t.Helper()
	b := &blockingIndexer{
		arrived: make(chan struct{}, 64),
		release: make(chan struct{}),
		answer:  answer,
	}
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	return b, srv
}

// ok is the common answer: 200 with this body.
func ok(body string) func(string) (int, string) {
	return func(string) (int, string) { return http.StatusOK, body }
}

// inFlight starts n identical callers, waits until one has reached the server,
// gives the rest room to pile onto the same flight, then lets everything
// return. The sleep is the only way to observe "they all got there": there is
// no hook that says a goroutine is parked inside singleflight.
func (b *blockingIndexer) runConcurrently(callers int, call func(i int)) {
	var ready, done sync.WaitGroup
	ready.Add(callers)
	done.Add(callers)
	for i := 0; i < callers; i++ {
		go func(i int) {
			defer done.Done()
			ready.Done()
			call(i)
		}(i)
	}
	ready.Wait()
	<-b.arrived
	time.Sleep(300 * time.Millisecond)
	close(b.release)
	done.Wait()
}

func TestConcurrentIdenticalQueriesMakeOneRoundTrip(t *testing.T) {
	const callers = 8

	b, srv := newBlockingIndexer(t, ok(`{"data":{"latestBlockHeight":445027}}`))
	c := NewClient(srv.URL)

	heights := make([]int, callers)
	errs := make([]error, callers)
	b.runConcurrently(callers, func(i int) {
		heights[i], errs[i] = c.LatestBlockHeight(context.Background())
	})

	if got := atomic.LoadInt64(&b.hits); got != 1 {
		t.Errorf("%d callers asking the same question cost %d upstream requests, want 1", callers, got)
	}
	for i := range heights {
		if errs[i] != nil {
			t.Errorf("caller %d: %v", i, errs[i])
			continue
		}
		if heights[i] != 445027 {
			t.Errorf("caller %d got height %d, want 445027: the shared answer was not decoded into every destination",
				i, heights[i])
		}
	}
}

// The other half: collapsing must be keyed tightly enough that two different
// questions are still two requests. A key that over-matches is a correctness
// bug, not a saving.
func TestDifferentQueriesAreNotCollapsed(t *testing.T) {
	b, srv := newBlockingIndexer(t, func(query string) (int, string) {
		if strings.Contains(query, "latestBlockHeight") {
			return http.StatusOK, `{"data":{"latestBlockHeight":1}}`
		}
		return http.StatusOK, `{"data":{"getSupply":{"denom":"ugnot","height":1,"total":"1","locked":"0","spendable":"1"}}}`
	})
	c := NewClient(srv.URL)

	b.runConcurrently(2, func(i int) {
		if i == 0 {
			c.LatestBlockHeight(context.Background())
			return
		}
		c.GetSupply(context.Background(), "ugnot")
	})

	if got := atomic.LoadInt64(&b.hits); got != 2 {
		t.Errorf("two different queries cost %d upstream requests, want 2", got)
	}
}

// Same query, different variables, is a different question too.
func TestSameQueryDifferentVarsAreNotCollapsed(t *testing.T) {
	b, srv := newBlockingIndexer(t, ok(`{"data":{"getSupply":{"denom":"ugnot","height":1,"total":"1","locked":"0","spendable":"1"}}}`))
	c := NewClient(srv.URL)

	denoms := []string{"ugnot", "ufoo"}
	b.runConcurrently(len(denoms), func(i int) {
		c.GetSupply(context.Background(), denoms[i])
	})

	if got := atomic.LoadInt64(&b.hits); got != 2 {
		t.Errorf("two denominations cost %d upstream requests, want 2", got)
	}
}

// Deduplication is in-flight only. Once an answer has been delivered the next
// caller asks again, because this change deliberately adds no TTL cache: which
// read paths are immutable is a separate and larger question.
func TestCollapsingIsInFlightOnlyAndCachesNothing(t *testing.T) {
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"latestBlockHeight":7}}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	for i := 0; i < 3; i++ {
		if _, err := c.LatestBlockHeight(context.Background()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt64(&hits); got != 3 {
		t.Errorf("three sequential calls made %d requests, want 3: something is caching answers", got)
	}
}

// A shared failure is shared, which is the point: N callers see one upstream
// error rather than each earning their own 429.
func TestConcurrentIdenticalQueriesShareOneRefusal(t *testing.T) {
	const callers = 6

	b, srv := newBlockingIndexer(t, func(string) (int, string) {
		return http.StatusTooManyRequests, onyxRateLimitBody
	})
	c := NewClient(srv.URL)

	b.runConcurrently(callers, func(int) {
		c.LatestBlockHeight(context.Background())
	})

	if got := atomic.LoadInt64(&b.hits); got != 1 {
		t.Errorf("%d callers cost %d refusals, want 1", callers, got)
	}
	if _, paused := c.PausedUntil(); !paused {
		t.Error("the shared refusal must still trip the breaker")
	}
}

// A caller leaving must not take the flight with it.
//
// The first reader to close their tab cancels the request, and if the flight is
// bound to that caller's context then every other reader waiting on the same
// answer gets a cancellation they never asked for, and the breaker records it
// as the indexer being slow. The request belongs to the flight.
func TestACancelledCallerDoesNotPoisonTheOthers(t *testing.T) {
	b, srv := newBlockingIndexer(t, ok(`{"data":{"latestBlockHeight":445027}}`))
	c := NewClient(srv.URL)

	quitter, abandon := context.WithCancel(context.Background())

	var quitterErr, stayerErr error
	var stayerHeight int
	var done sync.WaitGroup

	// Ordered, so the caller that leaves is provably the one that started the
	// flight. Started together it would be whichever goroutine won, and the
	// regression this pins would only show up half the time.
	done.Add(1)
	go func() {
		defer done.Done()
		_, quitterErr = c.LatestBlockHeight(quitter)
	}()
	<-b.arrived

	done.Add(1)
	go func() {
		defer done.Done()
		stayerHeight, stayerErr = c.LatestBlockHeight(context.Background())
	}()
	time.Sleep(300 * time.Millisecond)

	abandon()
	time.Sleep(100 * time.Millisecond)
	close(b.release)
	done.Wait()

	if !errors.Is(quitterErr, context.Canceled) {
		t.Errorf("the caller that left got %v, want its own context.Canceled", quitterErr)
	}
	if stayerErr != nil {
		t.Fatalf("the caller that stayed got %v; it inherited somebody else's cancellation", stayerErr)
	}
	if stayerHeight != 445027 {
		t.Errorf("the caller that stayed got height %d, want 445027", stayerHeight)
	}
	if got := atomic.LoadInt64(&b.hits); got != 1 {
		t.Errorf("%d upstream requests, want 1", got)
	}
}

// The backoff ladder counts refusals, not the callers that heard about one.
//
// Collapsing hands the same refusal to every waiter, and if each of them
// advanced the exponent then the stand-down would scale with the width of the
// fan-out rather than with how often the upstream actually said no: six readers
// hitting one 429 would jump straight to the ceiling.
func TestASharedRefusalAdvancesTheLadderOnce(t *testing.T) {
	const callers = 6

	b, srv := newBlockingIndexer(t, func(string) (int, string) {
		return http.StatusTooManyRequests, onyxRateLimitBody
	})
	c := NewClient(srv.URL)

	b.runConcurrently(callers, func(int) {
		c.LatestBlockHeight(context.Background())
	})

	until, paused := c.PausedUntil()
	if !paused {
		t.Fatal("the shared refusal did not trip the breaker")
	}
	// One notch: the 300s the server asked for, plus jitter. Six notches would
	// be 300s << 5, which the ceiling clamps to 15 minutes.
	if wait := time.Until(until); wait > 331*time.Second {
		t.Errorf("paused for %s after one refusal shared by %d callers; want about 300s, so the ladder moved %d times",
			wait.Round(time.Second), callers, callers)
	}
}
