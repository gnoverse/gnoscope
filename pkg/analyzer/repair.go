package analyzer

import (
	"fmt"
	"log"
)

// RepairResult is what one RepairCurrentSource pass found and did.
type RepairResult struct {
	// Checked is how many paths had a stored newest successful submission to
	// compare against.
	Checked int
	// Repaired lists the paths whose current state differed from their newest
	// successful submission and was rewritten from it.
	Repaired []string
	// StampWrong and FilesWrong break Repaired down: the packages row named
	// another submission, and the file set or a body differed. A path can be
	// both.
	StampWrong, FilesWrong int
	// Removed lists the paths whose every submission failed but which still
	// had a current-state row; those rows were dropped.
	Removed []string
	// Skipped is how many paths could not be judged, because their newest
	// successful submission's files are not stored (the indexer did not
	// return them).
	Skipped int
}

// Paths is every path the pass changed, repaired or removed.
func (r RepairResult) Paths() []string {
	return append(append([]string{}, r.Repaired...), r.Removed...)
}

// RepairCurrentSource recomputes one network's current package state from
// each path's newest successful submission, and rewrites whatever differs.
//
// Before failed submissions stopped writing current state, a failed
// MsgAddPackage replaced the packages row (its stamp) and upserted its files
// over the live ones, and a redeploy that dropped a file left the old one in
// place. Neither is ever revisited by the forward sync, which only moves up
// from its cursor; with every submission's files stored, the correct state is
// known and this puts it back. The rewrite goes through the same writes a
// successful submission does (ReplacePackage, SetDependencies), so the search
// index, tags and dependency edges move with it.
//
// Touches no sync cursor: they are derived from package_submissions and the
// event tables, none of which this writes.
func (a *Analyzer) RepairCurrentSource(network string) (RepairResult, error) {
	var res RepairResult
	cands, err := a.db.RepairCandidates(network)
	if err != nil {
		return res, err
	}
	for _, c := range cands {
		if !c.HasNewest {
			if !c.HasCurrent {
				continue
			}
			if err := a.db.DropCurrentPackage(network, c.Path); err != nil {
				return res, fmt.Errorf("drop %s: %w", c.Path, err)
			}
			if err := a.db.SetDependencies(network, c.Path, nil); err != nil {
				return res, fmt.Errorf("drop dependencies of %s: %w", c.Path, err)
			}
			res.Removed = append(res.Removed, c.Path)
			continue
		}
		if !c.NewestStored {
			res.Skipped++
			continue
		}
		res.Checked++

		stampWrong := !c.HasCurrent || c.Current.Height != c.Newest.Height || c.Current.TxHash != c.Newest.TxHash
		want, err := a.db.SubmissionFileHashes(network, c.Newest.TxHash, c.Newest.MsgIndex)
		if err != nil {
			return res, err
		}
		have, err := a.db.CurrentFileHashes(network, c.Path)
		if err != nil {
			return res, err
		}
		filesWrong := !sameHashes(want, have)
		if !stampWrong && !filesWrong {
			continue
		}

		files, err := a.db.SubmissionFiles(network, c.Newest.TxHash, c.Newest.MsgIndex)
		if err != nil {
			return res, err
		}
		if err := a.db.ReplacePackage(network, c.Path, c.NewestName, c.NewestCreator, c.Newest.TxHash,
			c.Newest.Height, c.NewestTime, c.NewestIsRealm, files); err != nil {
			return res, fmt.Errorf("replace %s: %w", c.Path, err)
		}
		if err := a.db.SetDependencies(network, c.Path, a.ExtractImports(c.Path, files)); err != nil {
			return res, fmt.Errorf("dependencies of %s: %w", c.Path, err)
		}
		res.Repaired = append(res.Repaired, c.Path)
		if stampWrong {
			res.StampWrong++
		}
		if filesWrong {
			res.FilesWrong++
		}
		log.Printf("[%s] source repair: %s rewritten from block %d (stamp was %d/%s, stamp wrong %v, files wrong %v)",
			network, c.Path, c.Newest.Height, c.Current.Height, shortHash(c.Current.TxHash), stampWrong, filesWrong)
	}
	return res, nil
}

func sameHashes(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func shortHash(h string) string {
	if len(h) > 10 {
		return h[:10]
	}
	return h
}
