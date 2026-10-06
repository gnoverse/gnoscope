package syncer

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"
)

// The per-submission source backfill and the repair that follows it.
//
// Every MsgAddPackage synced from now on carries its files into
// submission_files (see analyzer.ProcessPackage). Submissions synced before
// that have a row and no files, and the forward sync will never revisit them:
// its cursor is MAX(block_height) of package_submissions and only moves up.
// So this walk fetches them again, oldest first, a bounded height range at a
// time, under its own cursor in sync_state. It writes submission_files and
// blobs only, so it moves none of the derived cursors.
//
// Once nothing is left to fetch, RepairCurrentSource runs once per network and
// puts back any current state a failed submission overwrote before that stopped.

const (
	// defaultSubsrcBatch is how many submissions one indexer request covers. The
	// request carries every file body in the height range those submissions
	// span: about 24 KB a submission on gnoland1, so a batch is around a
	// megabyte, well inside the sync client's two-minute budget.
	defaultSubsrcBatch = 40
	// defaultSubsrcBatchesPerPass bounds one sync pass, so a database with the whole
	// history to fetch spreads it over a few passes instead of holding up the
	// forward sync behind it.
	defaultSubsrcBatchesPerPass = 5
	// defaultSubsrcPause separates two requests in one pass. The public indexer
	// answers a burst with 429s; a failed request ends the pass, and the
	// client's breaker covers the next ones.
	defaultSubsrcPause = 500 * time.Millisecond

	// subsrcRepairVersion names the repair recipe. Bumping it reruns the
	// repair once on every network.
	subsrcRepairVersion = "1"
)

// SubsrcCursorKey is the sync_state key holding the height up to which the
// submission-source backfill has fetched on one network.
func SubsrcCursorKey(network string) string { return "subsrc_backfill_cursor:" + network }

// SubsrcRepairKey is the sync_state key recording that the current-state
// repair ran on one network, and what it found.
func SubsrcRepairKey(network string) string { return "subsrc_repair:" + network }

// SetOnSourceRepaired registers fn to be told which paths a repair rewrote,
// so the response cache can drop what it holds for them. Set before the
// first SyncAll.
func (s *Syncer) SetOnSourceRepaired(fn func(network string, paths []string)) {
	s.onSourceRepaired = fn
}

// backfillSubmissionSource fetches the files of submissions that have none,
// and reports whether nothing is left to fetch.
func (s *Syncer) backfillSubmissionSource(ctx context.Context) (done bool) {
	cursor := -1
	if v, err := s.db.GetSyncState(SubsrcCursorKey(s.networkID)); err != nil {
		log.Printf("[%s] submission source backfill: %v", s.networkID, err)
		return false
	} else if v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cursor = n
		}
	}

	for batch := 0; batch < s.subsrcBatchesPerPass; batch++ {
		if batch > 0 {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(s.subsrcPause):
			}
		}
		refs, err := s.db.SubmissionsMissingFiles(s.networkID, cursor, s.subsrcBatch)
		if err != nil {
			log.Printf("[%s] submission source backfill: %v", s.networkID, err)
			return false
		}
		if len(refs) == 0 {
			return true
		}
		from, to := refs[0].Height, refs[len(refs)-1].Height
		start := time.Now()
		txs, err := s.client.GetPackagesInRange(ctx, from, to)
		if err != nil {
			// The cursor stays where it is: the next pass asks again.
			log.Printf("[%s] submission source backfill %d..%d: %v", s.networkID, from, to, err)
			return false
		}
		stored := 0
		for _, tx := range txs {
			for msgIndex, msg := range tx.Messages {
				if msg.Value.Typename != "MsgAddPackage" || msg.Value.Package == nil {
					continue
				}
				ok, err := s.db.AddSubmissionFiles(s.networkID, tx.Hash, msgIndex, msg.Value.Package.Path, msg.Value.Package.Files)
				if err != nil {
					log.Printf("[%s] submission source backfill %s: %v", s.networkID, tx.Hash, err)
					return false
				}
				if ok {
					stored++
				}
			}
		}
		// What the indexer did not return is counted and passed over, never
		// retried forever: the cursor moves past the range either way, and
		// the repair skips a path whose newest successful submission has no
		// files.
		missing := 0
		for _, r := range refs {
			if ok, err := s.db.HasSubmissionFiles(s.networkID, r.TxHash, r.MsgIndex); err == nil && !ok {
				missing++
			}
		}
		if err := s.db.SetSyncState(SubsrcCursorKey(s.networkID), strconv.Itoa(to)); err != nil {
			log.Printf("[%s] submission source backfill cursor: %v", s.networkID, err)
			return false
		}
		cursor = to
		log.Printf("[%s] submission source backfill: blocks %d..%d, %d submission(s) asked, %d stored, %d not returned, in %s",
			s.networkID, from, to, len(refs), stored, missing, time.Since(start).Round(time.Millisecond))
	}
	return false
}

// repairSubmissionSource runs the current-state repair once per network,
// after the backfill has nothing left to fetch, and logs what it found. The
// log line is the record: on a server that synced before failed submissions
// stopped writing current state, it is the count of paths that were wrong.
func (s *Syncer) repairSubmissionSource() {
	if v, err := s.db.GetSyncState(SubsrcRepairKey(s.networkID)); err != nil || versionOf(v) == subsrcRepairVersion {
		return
	}
	start := time.Now()
	res, err := s.analyzer.RepairCurrentSource(s.networkID)
	if err != nil {
		log.Printf("[%s] source repair: %v", s.networkID, err)
		return
	}
	summary := fmt.Sprintf("v%s checked=%d repaired=%d stamp_wrong=%d files_wrong=%d removed=%d skipped=%d at=%s",
		subsrcRepairVersion, res.Checked, len(res.Repaired), res.StampWrong, res.FilesWrong,
		len(res.Removed), res.Skipped, time.Now().UTC().Format(time.RFC3339))
	log.Printf("[%s] source repair: %d of %d paths were wrong and were rewritten from their newest successful submission "+
		"(%d with the wrong stamp, %d with the wrong files), %d removed because every submission there failed, "+
		"%d skipped without stored files, in %s",
		s.networkID, len(res.Repaired), res.Checked, res.StampWrong, res.FilesWrong, len(res.Removed), res.Skipped,
		time.Since(start).Round(time.Millisecond))
	if paths := res.Paths(); len(paths) > 0 && s.onSourceRepaired != nil {
		s.onSourceRepaired(s.networkID, paths)
	}
	if err := s.db.SetSyncState(SubsrcRepairKey(s.networkID), summary); err != nil {
		log.Printf("[%s] source repair marker: %v", s.networkID, err)
	}
}

// versionOf reads the recipe version off a repair marker ("v1 checked=...").
func versionOf(marker string) string {
	if len(marker) < 2 || marker[0] != 'v' {
		return ""
	}
	end := 1
	for end < len(marker) && marker[end] != ' ' {
		end++
	}
	return marker[1:end]
}
