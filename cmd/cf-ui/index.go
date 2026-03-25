// cmd/cf-ui/index.go — campfire list handler for the index (/) route.
//
// The index page lists all campfires the operator is a member of, with
// live unread counts updated via SSE unread events.
package main

import (
	"log/slog"
	"net/http"
	"time"
)

// CampfireEntry is the view-model for a single campfire in the list.
type CampfireEntry struct {
	// ID is the campfire ID (hex string).
	ID string
	// DisplayName is the campfire description, or the first 12 chars of the ID
	// if no description is set.
	DisplayName string
	// MemberCount is the number of known peer endpoints for this campfire.
	// The local operator is always member 1; peers add to the count.
	MemberCount int
	// LastActivityAt is the timestamp of the most recent message, in nanoseconds.
	// Zero if no messages exist.
	LastActivityAt int64
	// UnreadCount is the number of messages with timestamp > read cursor.
	UnreadCount int
	// HasRecentActivity is true if LastActivityAt is within the last 24 hours.
	HasRecentActivity bool
}

// CampfireLister provides the data needed to render the campfire list.
// Implemented by a store adapter that wraps MembershipStore + MessageStore + PeerStore.
// Tests inject a stub implementation.
type CampfireLister interface {
	// ListCampfires returns all campfires the operator is a member of,
	// populated with display data.
	ListCampfires() ([]CampfireEntry, error)
}

// indexData is the template data for the index page.
type indexData struct {
	Title      string
	Version    string
	Campfires  []CampfireEntry
	HasAny     bool
}

// handleIndexWithStore returns an http.HandlerFunc that renders the campfire
// list page using the provided CampfireLister. If lister is nil, an empty list
// is shown (the "no campfires yet" state).
func handleIndexWithStore(logger *slog.Logger, lister CampfireLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		var campfires []CampfireEntry
		if lister != nil {
			var err error
			campfires, err = lister.ListCampfires()
			if err != nil {
				logger.Error("listing campfires", "err", err)
				// Fall through with empty list — degraded but functional.
			}
		}

		data := indexData{
			Title:     "Campfire",
			Version:   Version,
			Campfires: campfires,
			HasAny:    len(campfires) > 0,
		}
		if err := renderPage(w, "index.html", data); err != nil {
			logger.Error("template error", "template", "index.html", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	}
}

// formatLastActivity formats a nanosecond timestamp as a human-readable relative time.
// Returns empty string if ts is zero.
func formatLastActivity(ts int64) string {
	if ts == 0 {
		return ""
	}
	t := time.Unix(0, ts)
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return "1 min ago"
		}
		return itoa(mins) + " mins ago"
	case d < 24*time.Hour:
		hrs := int(d.Hours())
		if hrs == 1 {
			return "1 hour ago"
		}
		return itoa(hrs) + " hours ago"
	case d < 7*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "yesterday"
		}
		return itoa(days) + " days ago"
	default:
		return t.Format("Jan 2")
	}
}

// itoa converts an int to a decimal string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
