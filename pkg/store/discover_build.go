package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/moul/mygnoscan/pkg/discover"
	"github.com/moul/mygnoscan/pkg/glossary"
)

// Building the Discover feed on the tick.
//
// Each kind is a source query, an emitter and the gate, in that order, and the
// gate is not advisory: an event whose generated prose fails a grounding check
// is dropped and counted, never stored. Storing it would put a sentence in
// front of a reader that asserts something the database does not contain, which
// is the single failure this whole feature is built to avoid, and it would do
// so in a table whose rows are append-only and whose ids feed readers dedupe
// on, so it could not be quietly corrected later either.

// DiscoverWindow is how far back a build looks.
//
// Thirty days, and it is not a backfill limit: every source table is already
// fully synced, so the first tick builds whatever history exists inside the
// window. On a chain younger than the window that is all of it.
const DiscoverWindow = 30 * 24 * time.Hour

// DiscoverBuildResult is what one build did, per network.
type DiscoverBuildResult struct {
	Network  string
	Built    int // events produced by the emitters
	Inserted int // rows actually new
	// Rejected are events the gate refused. A non-zero count here is a bug in
	// an emitter or a source query, never a data problem, so it is surfaced
	// rather than swallowed.
	Rejected   int
	Violations []string
}

// RefreshDiscoverEvents rebuilds the feed for each network.
//
// Idempotent by construction: ids are a pure function of the event, so a
// rebuild re-derives the same rows and inserts none of them. That is what makes
// running this on every tick cheap and safe rather than something that needs a
// cursor.
func (d *DB) RefreshDiscoverEvents(networks []string) ([]DiscoverBuildResult, error) {
	g := glossary.Get()
	if g == nil {
		// Loudly, not silently. Without the glossary there is no headword list,
		// so G5 (no jargon in the line a skimmer reads) cannot run, and a build
		// that quietly skipped one of the checks would produce exactly the
		// output the checks exist to catch.
		return nil, fmt.Errorf("glossary not loaded: the grounding gate cannot run without it")
	}
	since := time.Now().UTC().Add(-DiscoverWindow).Format(time.RFC3339)
	builtAt := time.Now().UTC().Format(time.RFC3339)

	out := make([]DiscoverBuildResult, 0, len(networks))
	for _, network := range networks {
		res := DiscoverBuildResult{Network: network}
		var events []DiscoverEvent

		for _, source := range []func(string, string) ([]discoverCandidate, error){
			d.sourcePackageDeployed,
			d.sourceDeployerFirst,
			d.sourceChainSpike,
		} {
			candidates, err := source(network, since)
			if err != nil {
				return out, fmt.Errorf("%s: %w", network, err)
			}
			for _, c := range candidates {
				facts, layers := c.Input.Emit()
				if vs := discover.Ground(facts, layers, g.Order); len(vs) > 0 {
					res.Rejected++
					// One example per kind is enough to debug from; a hundred
					// identical violations in a log is a reason to stop reading
					// logs.
					if res.Rejected <= 3 {
						res.Violations = append(res.Violations,
							fmt.Sprintf("%s %s: %s", c.Kind, c.Subject, vs[0].Error()))
					}
					continue
				}
				e, err := c.event(facts, layers, network, builtAt)
				if err != nil {
					return out, fmt.Errorf("%s %s: %w", network, c.Kind, err)
				}
				events = append(events, e)
			}
		}

		res.Built = len(events)
		n, err := d.UpsertDiscoverEvents(events)
		if err != nil {
			return out, fmt.Errorf("%s: %w", network, err)
		}
		res.Inserted = n
		out = append(out, res)
	}
	return out, nil
}

// discoverCandidate is one event before the gate has seen it.
type discoverCandidate struct {
	Kind      string
	Subject   string // the id's subject: a tx hash, an address, a day
	Ordinal   int
	At        string
	Height    int64
	Actor     string
	Target    string
	Namespace string
	Evidence  string
	FirstEver bool
	// Reach is unique actors and never a call count: a bot making thousands of
	// calls from one address must not be able to buy the top slot.
	Reach int64
	// Magnitude is the kind's natural quantity, raw. It is divided by that
	// kind's own floor when scored, so the number here stays the thing the
	// chain said rather than a ratio nobody can check.
	Magnitude float64
	Input     interface {
		Emit() (discover.Facts, discover.Layers)
	}
}

func (c discoverCandidate) event(facts discover.Facts, layers discover.Layers, network, builtAt string) (DiscoverEvent, error) {
	factsJSON, err := json.Marshal(facts)
	if err != nil {
		return DiscoverEvent{}, err
	}
	layersJSON, err := json.Marshal(layers)
	if err != nil {
		return DiscoverEvent{}, err
	}
	return DiscoverEvent{
		Network:    network,
		ID:         EventID(network, c.Kind, c.Subject, c.Ordinal),
		ScoreBase:  discover.ScoreBase(c.Kind, c.FirstEver, c.Reach, c.Magnitude),
		Kind:       c.Kind,
		At:         c.At,
		Height:     c.Height,
		Actor:      c.Actor,
		Target:     c.Target,
		Namespace:  c.Namespace,
		Facts:      factsJSON,
		Layers:     layersJSON,
		EvidenceTx: c.Evidence,
		FirstEver:  c.FirstEver,
		Reach:      c.Reach,
		Magnitude:  c.Magnitude,
		BuiltAt:    builtAt,
	}, nil
}

// sourcePackageDeployed: one event per path, at its earliest successful
// submission.
//
// The correlated subquery is what makes it one event per path rather than one
// per submission: 77 of mainnet's paths have been resubmitted, and a reader
// should be told once that a package was published, not once per attempt.
//
// block_height > 0 is the load-bearing filter and not a tidiness one. Without
// it, day one of the feed is 89 genesis packages all stamped with the same
// timestamp, which is 89 rows saying nothing happened.
func (d *DB) sourcePackageDeployed(network, since string) ([]discoverCandidate, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	rows, err := d.db.Query(`
		SELECT s.tx_hash, s.msg_index, s.path, s.creator, s.block_height, s.block_time,
		       s.is_realm, s.num_files,
		       COALESCE(u.name, ''),
		       (SELECT 1 FROM first_seen f
		         WHERE f.network = s.network AND f.kind = ? AND f.subject = s.creator
		           AND f.height = s.block_height)
		  FROM package_submissions s
		  LEFT JOIN users u ON u.network = s.network AND u.address = s.creator
		                   AND u.deleted = 0 AND u.alias = 0
		 WHERE s.network = ? AND s.success = 1
		   AND s.block_height > 0
		   AND s.block_time >= ?
		   AND s.block_height = (SELECT MIN(s2.block_height) FROM package_submissions s2
		                          WHERE s2.network = s.network AND s2.path = s.path AND s2.success = 1)
		 ORDER BY s.block_time DESC`, FirstSeenDeployer, network, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []discoverCandidate
	for rows.Next() {
		var txHash, path, creator, blockTime, label string
		var msgIndex, numFiles int
		var height int64
		var isRealm bool
		var firstEver *int
		if err := rows.Scan(&txHash, &msgIndex, &path, &creator, &height, &blockTime,
			&isRealm, &numFiles, &label, &firstEver); err != nil {
			return nil, err
		}
		ns, _ := splitGnoPath(path)
		out = append(out, discoverCandidate{
			Kind:      "package.deployed",
			Subject:   txHash,
			Ordinal:   msgIndex,
			At:        blockTime,
			Height:    height,
			Actor:     creator,
			Target:    path,
			Namespace: ns,
			Evidence:  txHash,
			FirstEver: firstEver != nil,
			Magnitude: float64(numFiles),
			Input: discover.PackageDeployed{
				Path:       path,
				Creator:    creator,
				ActorLabel: atLabel(label),
				Height:     height,
				IsRealm:    isRealm,
				NumFiles:   numFiles,
				FirstEver:  firstEver != nil,
			},
		})
	}
	return out, rows.Err()
}

// sourceDeployerFirst: an address publishing for the first time.
//
// Read from first_seen rather than computed, which is the whole reason that
// table exists: the live form is a GROUP BY ... HAVING MIN(height) over every
// submission, and this is a range scan on (network, kind, at).
//
// height > 0 excludes genesis, for the same reason the deploy source does.
// first_seen itself is right to record a genesis deployer: "when did this
// address first publish" has an answer there. But "a new builder arrived" is
// not true of an account that was present before the chain produced a block,
// and 89 of them arriving at one timestamp is the day-one noise §2.2 describes.
func (d *DB) sourceDeployerFirst(network, since string) ([]discoverCandidate, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// The all-time count, which is the only thing that licenses layer 3 saying
	// how many people have ever published here. Counted, not guessed: the gate
	// rejects the sentence outright if this is not in the facts.
	var distinct int
	if err := d.db.QueryRow(
		`SELECT COUNT(*) FROM first_seen WHERE network = ? AND kind = ?`,
		network, FirstSeenDeployer).Scan(&distinct); err != nil {
		return nil, err
	}

	rows, err := d.db.Query(`
		SELECT f.subject, f.at, f.height, COALESCE(u.name, ''),
		       COALESCE(s.path, ''), COALESCE(s.is_realm, 0), COALESCE(s.tx_hash, '')
		  FROM first_seen f
		  LEFT JOIN users u ON u.network = f.network AND u.address = f.subject
		                   AND u.deleted = 0 AND u.alias = 0
		  LEFT JOIN package_submissions s ON s.network = f.network AND s.creator = f.subject
		                                 AND s.block_height = f.height AND s.success = 1
		 WHERE f.network = ? AND f.kind = ? AND f.at >= ?
		   AND f.height > 0
		 ORDER BY f.at DESC`, network, FirstSeenDeployer, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []discoverCandidate
	for rows.Next() {
		var address, at, label, path, txHash string
		var height int64
		var isRealm bool
		if err := rows.Scan(&address, &at, &height, &label, &path, &isRealm, &txHash); err != nil {
			return nil, err
		}
		ns, name := splitGnoPath(path)
		out = append(out, discoverCandidate{
			Kind:      "deployer.first",
			Subject:   address,
			At:        at,
			Height:    height,
			Actor:     address,
			Target:    path,
			Namespace: ns,
			Evidence:  txHash,
			FirstEver: true,
			Magnitude: 1,
			Input: discover.DeployerFirst{
				Address:           address,
				ActorLabel:        atLabel(label),
				PackageName:       name,
				IsRealm:           isRealm,
				NetworkLabel:      network,
				DistinctDeployers: distinct,
			},
		})
	}
	return out, rows.Err()
}

// sourceChainSpike: a day on which unusually many addresses appeared.
//
// The series comes from first_seen, deliberately, and not from
// GetNewAddressTimeSeries: that one UNION ALLs four tables over all history
// with no time predicate inside the union, which is correct and O(every row)
// forever. It is milliseconds today and it is the next query that has to be
// given a rollup.
//
// The baseline is clipped to the chain's own first indexed day, which is the
// part of the detector everyone simplifies away: days before the chain existed
// are absence, not quiet days, and counting them as zeros makes the first real
// day fire against a baseline of nothing.
func (d *DB) sourceChainSpike(network, since string) ([]discoverCandidate, error) {
	d.mu.RLock()
	rows, err := d.db.Query(`
		SELECT substr(at, 1, 10) AS day, COUNT(*), MIN(height)
		  FROM first_seen
		 WHERE network = ? AND kind = ?
		 GROUP BY day
		 ORDER BY day`, network, FirstSeenAddress)
	if err != nil {
		d.mu.RUnlock()
		return nil, err
	}
	type dayRow struct {
		day    string
		n      int
		height int64
	}
	var series []dayRow
	for rows.Next() {
		var r dayRow
		if err := rows.Scan(&r.day, &r.n, &r.height); err != nil {
			rows.Close()
			d.mu.RUnlock()
			return nil, err
		}
		series = append(series, r)
	}
	err = rows.Err()
	rows.Close()
	d.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if len(series) == 0 {
		return nil, nil
	}

	points := make([]discover.Point, len(series))
	for i, r := range series {
		points[i] = discover.Point{Day: r.day, X: float64(r.n)}
	}
	// The chain's own first day, which is the earliest day in the series by
	// construction: first_seen holds first appearances, so there is no earlier
	// day with participants to have missed.
	start := series[0].day
	verdicts := discover.Detect(points, discover.SpikeFloors["chain.spike"], start)

	byDay := map[string]dayRow{}
	for _, r := range series {
		byDay[r.day] = r
	}
	sinceDay := since
	if len(sinceDay) > 10 {
		sinceDay = sinceDay[:10]
	}

	var out []discoverCandidate
	for _, v := range verdicts {
		if !v.Fired || v.Day < sinceDay {
			continue
		}
		r := byDay[v.Day]
		out = append(out, discoverCandidate{
			Kind:    "chain.spike",
			Subject: v.Day,
			// Midnight UTC of the day it describes. A spike is a statement
			// about a whole day, so any hour inside it would be a false
			// precision, and the feed orders by this.
			At:        v.Day + "T00:00:00Z",
			Height:    r.height,
			Reach:     int64(r.n),
			Magnitude: float64(r.n),
			Input: discover.ChainSpike{
				Day:            v.Day,
				NetworkLabel:   network,
				NewAddresses:   r.n,
				BaselineMedian: int(v.Median),
				Ratio:          v.Ratio,
			},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out, nil
}

// splitGnoPath returns the namespace and the human name of a gno path.
// "gno.land/r/moul/hello" is "moul" and "hello".
//
// It defers to discover.SplitPath rather than deriving it, because this used to
// be a second implementation and the two disagreed the moment one was fixed. A
// version segment is not a name, and this copy still called
// gno.land/p/moul/x/vm/riscv/v0 "v0" while the emitter's copy called it "riscv",
// so the fact and the sentence built from it named different things.
func splitGnoPath(path string) (namespace, name string) {
	if path == "" {
		return "", ""
	}
	return discover.SplitPath(path)
}

// atLabel renders a registered name the way the emitters expect it, and an
// unregistered address as the empty string rather than as the address: the
// templates branch on emptiness to choose between naming somebody and saying
// "somebody".
func atLabel(name string) string {
	if name == "" {
		return ""
	}
	return "@" + name
}
