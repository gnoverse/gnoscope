package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGovDAOPages serves gov/dao's list render the way the chain does: five a
// page, newest first, with a pager line, from `total` proposals. onPage lets a
// case replace one page's answer (an error, or a clamped repeat).
func fakeGovDAOPages(t *testing.T, total int, onPage func(page int) (string, bool)) string {
	t.Helper()
	render := func(page int) string {
		var b strings.Builder
		b.WriteString("# GovDAO\n## Proposals\n")
		for id := total - 1 - (page-1)*5; id >= 0 && id > total-1-page*5; id-- {
			fmt.Fprintf(&b, "### [Prop #%d - Proposal %d](/r/gov/dao:%d)\nAuthor: [@aeddi](/u/aeddi)\n\nStatus: ACCEPTED\n\nTiers eligible to vote: T1, T2, T3\n\n---\n\n", id, id, id)
		}
		b.WriteString("**1** | [2](?page=2)\n")
		return b.String()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct{ Path, Data string } `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		data, _ := base64.StdEncoding.DecodeString(req.Params.Data)
		page := 1
		if i := strings.Index(string(data), "?page="); i >= 0 {
			fmt.Sscanf(string(data)[i+len("?page="):], "%d", &page)
		}
		md := render(page)
		if onPage != nil {
			if alt, fail := onPage(page); fail {
				w.WriteHeader(http.StatusInternalServerError)
				return
			} else if alt != "" {
				md = alt
			}
		}
		fmt.Fprintf(w, `{"result":{"response":{"ResponseBase":{"Data":%q}}}}`, base64.StdEncoding.EncodeToString([]byte(md)))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func ids(props []GovDAOProposalSummary) string {
	var s []string
	for _, p := range props {
		s = append(s, fmt.Sprint(p.ID))
	}
	return strings.Join(s, ",")
}

func TestFetchGovDAOProposalListWalksEveryPage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		total  int
		onPage func(int) (string, bool)
		want   string
	}{
		// mainnet on 2026-10-01: eight proposals, page one showed 7 to 3.
		{"mainnet's eight", 8, nil, "7,6,5,4,3,2,1,0"},
		{"exactly one page", 5, nil, "4,3,2,1,0"},
		{"onyx's twenty-two", 22, nil, "21,20,19,18,17,16,15,14,13,12,11,10,9,8,7,6,5,4,3,2,1,0"},
		// A later page failing keeps what was read: the newest are the ones
		// with a vote still open.
		{"page two fails", 12, func(p int) (string, bool) { return "", p == 2 }, "11,10,9,8,7"},
		// A render that clamps past-the-end pages to the last one must not loop.
		{"clamped pages", 7, func(p int) (string, bool) {
			if p > 2 {
				return "### [Prop #1 - again](/r/gov/dao:1)\n### [Prop #0 - again](/r/gov/dao:0)\n", false
			}
			return "", false
		}, "6,5,4,3,2,1,0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fetchGovDAOProposalList(context.Background(), fakeGovDAOPages(t, tc.total, tc.onPage))
			if err != nil {
				t.Fatal(err)
			}
			if ids(got) != tc.want {
				t.Errorf("got %s, want %s", ids(got), tc.want)
			}
			for _, p := range got {
				if p.Status != "ACCEPTED" {
					t.Errorf("#%d lost its status: %+v", p.ID, p)
				}
			}
		})
	}
}

func TestFetchGovDAOProposalListFailsOnPageOne(t *testing.T) {
	url := fakeGovDAOPages(t, 8, func(p int) (string, bool) { return "", p == 1 })
	if _, err := fetchGovDAOProposalList(context.Background(), url); err == nil {
		t.Error("an unreadable first page reported success")
	}
}
