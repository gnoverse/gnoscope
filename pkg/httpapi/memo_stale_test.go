package httpapi

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A memo with a grace hands an expired value straight back and replaces it
// behind the caller. Without one, whoever arrives after the TTL lapses runs the
// fetch themselves — measured at 13.6s on a production proposal page against
// 0.40s for everyone who arrived while the list was fresh.
func TestAStaleMemoAnswersWithoutWaiting(t *testing.T) {
	const slow = 300 * time.Millisecond
	var calls int
	var mu sync.Mutex
	fetch := func(ctx context.Context) (string, bool) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		time.Sleep(slow)
		return map[bool]string{true: "first", false: "second"}[n == 1], true
	}

	m := newStaleWhileRevalidateMemo[string](20*time.Millisecond, time.Second, time.Minute)
	if got := m.get(context.Background(), "k", fetch); got != "first" {
		t.Fatalf("first get = %q, want first", got)
	}
	m.expireAll()

	start := time.Now()
	got := m.get(context.Background(), "k", fetch)
	elapsed := time.Since(start)

	if got != "first" {
		t.Errorf("stale get = %q, want the last good value", got)
	}
	if elapsed >= slow {
		t.Errorf("stale get took %v, want it to return immediately", elapsed)
	}

	// The replacement lands behind the caller, and the next get sees it.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v, _ := m.peek("k"); v == "second" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("the background refresh never stored its result")
}

// Past ttl+grace the value is too old to serve, and the caller waits for a
// real answer rather than being handed something an hour stale.
func TestAMemoStopsServingPastItsGrace(t *testing.T) {
	const slow = 200 * time.Millisecond
	fetch := func(ctx context.Context) (string, bool) {
		time.Sleep(slow)
		return "fresh", true
	}
	// A grace shorter than how far expireAll pushes retryAt into the past, so
	// the entry lands beyond ttl+grace.
	m := newStaleWhileRevalidateMemo[string](time.Millisecond, time.Second, 10*time.Millisecond)
	m.get(context.Background(), "k", func(ctx context.Context) (string, bool) { return "old", true })
	m.expireAll()

	start := time.Now()
	got := m.get(context.Background(), "k", fetch)
	if got != "fresh" {
		t.Errorf("get = %q, want the refetched value", got)
	}
	if elapsed := time.Since(start); elapsed < slow {
		t.Errorf("get returned in %v, want it to wait for the refetch", elapsed)
	}
}

// Without a grace nothing changes: whoever finds an expired entry waits. That
// is still right for the memos whose answer must not lag, so it stays the
// default rather than becoming something every caller has to opt out of.
func TestAMemoWithoutAGraceStillWaits(t *testing.T) {
	const slow = 200 * time.Millisecond
	m := newMemo[string](time.Millisecond, time.Second)
	m.get(context.Background(), "k", func(ctx context.Context) (string, bool) { return "old", true })
	m.expireAll()

	start := time.Now()
	got := m.get(context.Background(), "k", func(ctx context.Context) (string, bool) {
		time.Sleep(slow)
		return "fresh", true
	})
	if got != "fresh" {
		t.Errorf("get = %q, want fresh", got)
	}
	if elapsed := time.Since(start); elapsed < slow {
		t.Errorf("get returned in %v, want it to wait", elapsed)
	}
}

// Many readers arriving on a stale key start one refresh between them, not one
// each. Single-flight is the other half of why this type exists.
func TestAStaleMemoRefreshesOnce(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	fetch := func(ctx context.Context) (string, bool) {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		return "v", true
	}
	m := newStaleWhileRevalidateMemo[string](time.Millisecond, time.Second, time.Minute)
	m.get(context.Background(), "k", fetch)
	m.expireAll()

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.get(context.Background(), "k", fetch)
		}()
	}
	wg.Wait()
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Errorf("fetch ran %d times, want 2 — the first get and one shared refresh", calls)
	}
}
