package web

import (
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tkakkie/ribbitto/internal/web/middleware"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func rejectMemberLimit(w http.ResponseWriter, r *http.Request, limit *middleware.MemberRateLimiter, pages *pageRenderer) {
	// HTTP delay-seconds rounds up so subsecond budgets still give a useful hint.
	seconds := int64(math.Ceil(float64(limit.RefillInterval()) / float64(time.Second)))
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	if ordinaryFormSubmission(r) {
		pages.render(w, r, http.StatusTooManyRequests, view.TooManyRequests)
		return
	}
	w.WriteHeader(http.StatusTooManyRequests)
}

func ordinaryFormSubmission(r *http.Request) bool {
	// Navigation plus explicit HTML and form media types avoids treating fetch
	// defaults or missing browser metadata as a request for a whole page.
	if r.Method != http.MethodPost || r.Header.Get("Sec-Fetch-Mode") != "navigate" || len(r.Header.Values("HX-Request")) != 0 {
		return false
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (kind != "application/x-www-form-urlencoded" && kind != "multipart/form-data") {
		return false
	}
	html := false
	for _, value := range strings.Split(strings.Join(r.Header.Values("Accept"), ","), ",") {
		kind, params, err := mime.ParseMediaType(strings.TrimSpace(value))
		if err != nil || !strings.Contains(kind, "/") {
			return false
		}
		quality := 1.0
		if q, ok := params["q"]; ok {
			if !validAcceptQuality(q) {
				return false
			}
			quality, err = strconv.ParseFloat(q, 64)
			if err != nil || math.IsNaN(quality) || quality < 0 || quality > 1 {
				return false
			}
		}
		if kind == "text/html" {
			if quality == 0 {
				return false
			}
			html = true
		}
	}
	return html
}

func validAcceptQuality(q string) bool {
	// RFC 9110 qvalues allow at most three decimal digits, and only zeros after 1.
	if len(q) == 0 || (q[0] != '0' && q[0] != '1') {
		return false
	}
	if len(q) == 1 {
		return true
	}
	if len(q) > 5 || q[1] != '.' {
		return false
	}
	for _, digit := range q[2:] {
		if digit < '0' || digit > '9' || (q[0] == '1' && digit != '0') {
			return false
		}
	}
	return true
}
