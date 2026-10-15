package httpapi

// DefaultNetwork is the chain a visitor with no stored choice should land on:
// the first configured one.
//
// It is served into the document rather than fetched, because the answer has to
// be known before the first request for data goes out. Resolving it after
// /api/networks lands is too late: the first page load has already asked for a
// chain by then, and correcting the selector afterwards leaves the header
// naming one chain while the tiles hold another's numbers.
//
// Empty when nothing is configured, which the page reads as "keep your own
// fallback".
func (a *API) DefaultNetwork() string {
	if len(a.networks) == 0 {
		return ""
	}
	return a.networks[0].ID
}
