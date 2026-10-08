package genesis

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gnoverse/gnoscope/pkg/store"
)

// The sheet the gnoland-1 genesis was built from, pinned to a commit and to the
// SHA-256 that repository publishes for it in SHA256SUMS. Both change together
// or not at all: moving the pin is a decision, not drift.
//
// Checked 2026-10-08: 3,262,481 rows totalling 1,332,999,998.328067 GNOT, and
// the downloaded file hashes to SourceSHA256.
const (
	SourceCommit = "30ec18996779f0185966ddb628590008999dbecf"
	SourceURL    = "https://raw.githubusercontent.com/gnolang/independence-day/" + SourceCommit + "/mkgenesis/balances.txt.gz"
	SourceSHA256 = "3379977407b57e617da5d3dcb5b8f0aeb0a739bd51e152bdc1a60fbec0215ec3"
	// SourceRows is what the sheet's own README says it holds.
	SourceRows = 3262481
)

// ParseLine reads one sheet line:
//
//	g1…=332000000000000ugnot;vesting=318720000000000ugnot,1789225200,1852383600
//
// The vesting clause is optional. Anything else is an error rather than a
// silently skipped row: a sheet that does not parse is a sheet that changed.
func ParseLine(line string) (store.GenesisRow, error) {
	var row store.GenesisRow
	addr, rest, ok := strings.Cut(strings.TrimSpace(line), "=")
	if !ok {
		return row, errors.New("no '='")
	}
	hrp, data, err := Decode(addr)
	if err != nil || hrp != "g" || len(data) != 20 {
		return row, fmt.Errorf("bad address %q", addr)
	}
	copy(row.Addr[:], data)

	// An optional ";type=delayed" closes a vesting clause. Any other type is an
	// error: a schedule this code does not understand must not be drawn as one
	// it does.
	if before, typ, ok := strings.Cut(rest, ";type="); ok {
		if typ != "delayed" {
			return row, fmt.Errorf("vesting type %q", typ)
		}
		row.VestDelayed = true
		rest = before
	}
	bal, vest, hasVest := strings.Cut(rest, ";vesting=")
	if row.Ugnot, err = parseUgnot(bal); err != nil {
		return row, err
	}
	if hasVest {
		parts := strings.Split(vest, ",")
		if len(parts) != 3 {
			return row, fmt.Errorf("vesting clause %q", vest)
		}
		if row.VestUgnot, err = parseUgnot(parts[0]); err != nil {
			return row, err
		}
		if row.VestStart, err = strconv.ParseInt(parts[1], 10, 64); err != nil {
			return row, err
		}
		if row.VestEnd, err = strconv.ParseInt(parts[2], 10, 64); err != nil {
			return row, err
		}
		row.HasVesting = true
	}
	if row.VestDelayed && !hasVest {
		return row, errors.New("delayed type without a vesting clause")
	}
	return row, nil
}

func parseUgnot(s string) (int64, error) {
	n, ok := strings.CutSuffix(s, "ugnot")
	if !ok {
		return 0, fmt.Errorf("amount %q is not ugnot", s)
	}
	return strconv.ParseInt(n, 10, 64)
}

// Fetch downloads the sheet to a file in dir and returns its path, deleting it
// and failing unless the content hashes to want. Nothing is read from the file
// before that check passes.
func Fetch(ctx context.Context, client *http.Client, url, want, dir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: %s", url, resp.Status)
	}
	f, err := os.CreateTemp(dir, "genesis-*.gz")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		os.Remove(f.Name())
		return "", fmt.Errorf("checksum mismatch: got %s, want %s", got, want)
	}
	return f.Name(), nil
}

const batchSize = 20000

// Import streams a gzipped sheet into the store and writes the marker last.
// source and sha describe where it came from. The caller has verified sha.
func Import(db *store.DB, gz io.Reader, source, sha string) (int, error) {
	zr, err := gzip.NewReader(gz)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	if err := db.GenesisReset(); err != nil {
		return 0, err
	}
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	batch := make([]store.GenesisRow, 0, batchSize)
	total := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		row, err := ParseLine(line)
		if err != nil {
			return total, fmt.Errorf("row %d: %w", total+1, err)
		}
		batch = append(batch, row)
		if len(batch) == batchSize {
			if err := db.GenesisInsert(batch); err != nil {
				return total, err
			}
			total += len(batch)
			batch = batch[:0]
		}
	}
	if err := sc.Err(); err != nil {
		return total, err
	}
	if err := db.GenesisInsert(batch); err != nil {
		return total, err
	}
	total += len(batch)
	return total, db.GenesisMarkImported(source, sha, total)
}

// EnsureImported makes the table complete: a no-op when the marker already
// names this sheet, otherwise download, verify, import. It is meant to run once
// in the background at startup; the explorer answers "not loaded yet" until it
// finishes rather than guessing.
func EnsureImported(ctx context.Context, db *store.DB, client *http.Client) error {
	if sha, _, ok, err := db.GenesisImported(); err != nil {
		return err
	} else if ok && sha == SourceSHA256 {
		return nil
	}
	dir, err := os.MkdirTemp("", "gnoscope-genesis-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	path, err := Fetch(ctx, client, SourceURL, SourceSHA256, dir)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = Import(db, f, SourceURL, SourceSHA256)
	return err
}
