package contentcoding

import (
	"net/http"
	"strconv"
	"strings"
)

// Quality returns the advertised weight for a content coding. A specific
// entry overrides the wildcard, including when it explicitly opts out.
func Quality(r *http.Request, coding string) float64 {
	var wildcard float64
	for _, line := range r.Header.Values("Accept-Encoding") {
		for item := range strings.SplitSeq(line, ",") {
			name, parameters, _ := strings.Cut(item, ";")
			name = strings.TrimSpace(name)
			if strings.EqualFold(name, coding) {
				return weight(parameters)
			}
			if name == "*" {
				wildcard = weight(parameters)
			}
		}
	}
	return wildcard
}

func weight(parameters string) float64 {
	for parameter := range strings.SplitSeq(parameters, ";") {
		key, value, _ := strings.Cut(parameter, "=")
		if strings.EqualFold(strings.TrimSpace(key), "q") {
			q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err == nil && q >= 0 && q <= 1 {
				return q
			}
			return 0
		}
	}
	return 1
}
