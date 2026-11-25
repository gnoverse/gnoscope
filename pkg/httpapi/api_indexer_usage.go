package httpapi

import (
	"net/http"

	"github.com/gnoverse/gnoscope/pkg/reqmeter"
)

// What this process spends at the indexers, served as data.
//
// The open question on #525 is whether a short-TTL response cache would remove
// the rate-limit problem outright rather than treat it, and that cannot be
// answered without knowing how much of the 3,100-per-300-seconds per-IP budget
// this explorer actually spends and on which queries. #526 removed the 429s
// without measuring what was behind them; this is the measurement, and the
// would-have-hit figures per TTL are the answer that decides the cache.
//
// The meter is in pkg/reqmeter, on the indexer client rather than here, because
// it has to count the sync loop and the SSE poll loop as well as the request
// paths this package serves. See there for what bounds it.

// SetIndexerMeter points the API at a request meter.
//
// A setter, like SetHealthLog and SetTraffic, but for a different reason: there
// is no flag behind this one and nothing to enable. It exists so a test can
// hand over a meter it filled itself, rather than having to drive real HTTP
// traffic through the process-wide one and then read it back.
func (a *API) SetIndexerMeter(m *reqmeter.Meter) { a.indexerMeter = m }

// indexerUsage is the meter this instance reports. The process-wide one unless
// a test replaced it, because that is the one the clients were built with: the
// budget being measured is per IP, so a per-API meter would be counting a
// fraction of what the IP spends and reporting it against the whole ceiling.
func (a *API) indexerUsage() *reqmeter.Meter {
	if a.indexerMeter != nil {
		return a.indexerMeter
	}
	return reqmeter.Default()
}

// HandleIndexerUsage serves the outbound request meter.
//
// Never cached. It reports this server's own live counters, and a cached answer
// would describe the state the meter was in thirty seconds ago, which is the
// one question it exists to answer correctly. See cacheable() in cache.go,
// where this is the fifth endpoint in that class and the list says so.
func (a *API) HandleIndexerUsage(w http.ResponseWriter, r *http.Request) {
	JSONResponse(w, a.indexerUsage().Snapshot())
}
