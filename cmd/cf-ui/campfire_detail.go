// cmd/cf-ui/campfire_detail.go — Campfire detail page handlers.
//
// Routes:
//
//	GET /c/{id}            — full two-panel detail page
//	GET /c/{id}/messages   — HTML fragment: message feed (htmx partial + tag filter)
package main

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

// MessageStore is the subset of store.MessageStore used by the detail handlers.
// Using an interface keeps tests free of the real SQLite dependency.
type MessageStore interface {
	ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error)
}

// MembershipChecker reports whether an operator is a member of a campfire.
// Used to enforce BOLA/IDOR access control on campfire detail and send routes.
// If nil, all authenticated operators are allowed (open mode — for testing/dev).
type MembershipChecker interface {
	IsMember(campfireID, operatorEmail string) bool
}

// CampfireDetailHandlers holds dependencies for campfire detail routes.
type CampfireDetailHandlers struct {
	logger     *slog.Logger
	messages   MessageStore     // nil → stub (empty feed)
	csrf       *csrfStore       // non-nil → CSRF token injected into compose box
	membership MembershipChecker // nil → open access (no membership check)
}

// NewCampfireDetailHandlers creates a handler bundle.
// ms may be nil; in that case all campfires show an empty message feed.
// csrf may be nil; in that case the compose box sends an empty CSRF token.
func NewCampfireDetailHandlers(logger *slog.Logger, ms MessageStore) *CampfireDetailHandlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &CampfireDetailHandlers{logger: logger, messages: ms}
}

// WithCSRF attaches a csrfStore so the detail page can embed a CSRF token in the compose form.
func (h *CampfireDetailHandlers) WithCSRF(csrf *csrfStore) *CampfireDetailHandlers {
	h.csrf = csrf
	return h
}

// WithMembership attaches a MembershipChecker so that only campfire members can
// access detail and message routes. Without this, all authenticated operators
// can access any campfire (open/dev mode).
func (h *CampfireDetailHandlers) WithMembership(m MembershipChecker) *CampfireDetailHandlers {
	h.membership = m
	return h
}

// checkMembership returns false and writes a 403 if the caller is not a member.
// Returns true if membership is not configured (nil checker) or if the operator is a member.
func (h *CampfireDetailHandlers) checkMembership(w http.ResponseWriter, campfireID, operatorEmail string) bool {
	if h.membership == nil {
		return true
	}
	if !h.membership.IsMember(campfireID, operatorEmail) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// messageViewModel is the view-layer representation of a single message.
type messageViewModel struct {
	ID          string
	Sender      string   // display name — hex prefix of sender pubkey
	Instance    string   // tainted role/instance label, may be empty
	Tags        []string // sorted tag list
	Body        string   // decoded payload text (best-effort)
	Timestamp   int64    // nanoseconds; used for sorting
	TimeFmt     string   // human-readable: "14:32" same day, "Mon 14:32" otherwise
	Threaded    bool     // true when message has antecedents
	ThreadCount int      // number of antecedent IDs
}

// campfireDetailPageData is the template data for campfire.html.
type campfireDetailPageData struct {
	Title      string
	CampfireID string
	Version    string
	CSRFToken  string   // embedded in compose box form
	ValidTags  []string // tag selector options
	Messages   []messageViewModel
	ActiveTags []string // tags currently filtered (from query param)
	AllTags    []string // union of all tags seen in current feed
}

// messageFragmentData is the template data for the messages fragment.
type messageFragmentData struct {
	CampfireID string
	Messages   []messageViewModel
	ActiveTags []string
	AllTags    []string
	Empty      bool
}

// HandleDetail renders the full campfire detail page.
func (h *CampfireDetailHandlers) HandleDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	// BOLA/IDOR guard: only campfire members may view the detail page.
	identity, ok := IdentityFromContext(r.Context())
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if !h.checkMembership(w, id, identity.Email) {
		return
	}

	activeTags := parseTags(r.URL.Query().Get("tags"))
	msgs, allTags, err := h.loadMessages(id, activeTags)
	if err != nil {
		h.logger.Error("campfire detail: list messages", "campfire", id, "err", err)
		// Render the page with empty feed rather than a hard error.
		msgs = nil
		allTags = nil
	}

	// Read CSRF token injected by CSRFMiddleware (for the compose box form).
	csrfToken := CSRFTokenFromContext(r.Context())

	data := campfireDetailPageData{
		Title:      "Campfire — " + id,
		CampfireID: id,
		Version:    Version,
		CSRFToken:  csrfToken,
		ValidTags:  validSendTags,
		Messages:   msgs,
		ActiveTags: activeTags,
		AllTags:    allTags,
	}
	if err := renderPage(w, "campfire.html", data); err != nil {
		h.logger.Error("template error", "template", "campfire.html", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// HandleMessages returns an HTML fragment of the message feed.
// Used by htmx for tag filtering and initial load.
func (h *CampfireDetailHandlers) HandleMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	// BOLA/IDOR guard: only campfire members may fetch the message feed.
	identity, ok := IdentityFromContext(r.Context())
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if !h.checkMembership(w, id, identity.Email) {
		return
	}

	activeTags := parseTags(r.URL.Query().Get("tags"))
	msgs, allTags, err := h.loadMessages(id, activeTags)
	if err != nil {
		h.logger.Error("campfire messages fragment: list messages", "campfire", id, "err", err)
		msgs = nil
	}

	data := messageFragmentData{
		CampfireID: id,
		Messages:   msgs,
		ActiveTags: activeTags,
		AllTags:    allTags,
		Empty:      len(msgs) == 0,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := renderPage(w, "messages_fragment.html", data); err != nil {
		h.logger.Error("template error", "template", "messages_fragment.html", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// loadMessages fetches and converts messages for a campfire.
// Returns (viewModels, allTags, error).
func (h *CampfireDetailHandlers) loadMessages(campfireID string, tags []string) ([]messageViewModel, []string, error) {
	if h.messages == nil {
		return nil, nil, nil
	}

	var filter store.MessageFilter
	if len(tags) > 0 {
		filter = store.MessageFilter{Tags: tags}
	}

	records, err := h.messages.ListMessages(campfireID, 0, filter)
	if err != nil {
		return nil, nil, err
	}

	tagSet := make(map[string]struct{})
	vms := make([]messageViewModel, 0, len(records))
	for _, rec := range records {
		for _, t := range rec.Tags {
			tagSet[t] = struct{}{}
		}
		vms = append(vms, toViewModel(rec))
	}

	allTags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		allTags = append(allTags, t)
	}
	sortStrings(allTags)

	return vms, allTags, nil
}

// toViewModel converts a store.MessageRecord to a messageViewModel.
func toViewModel(r store.MessageRecord) messageViewModel {
	body := string(r.Payload)

	sender := r.Sender
	if len(sender) > 12 {
		sender = sender[:12]
	}
	if r.Instance != "" {
		// prefer instance label as display name
	}

	tags := make([]string, len(r.Tags))
	copy(tags, r.Tags)
	sortStrings(tags)

	ts := r.Timestamp
	timeFmt := formatNanoTimestamp(ts)

	return messageViewModel{
		ID:          r.ID,
		Sender:      sender,
		Instance:    r.Instance,
		Tags:        tags,
		Body:        body,
		Timestamp:   ts,
		TimeFmt:     timeFmt,
		Threaded:    len(r.Antecedents) > 0,
		ThreadCount: len(r.Antecedents),
	}
}

// parseTags splits a comma-separated tag string into a slice, trimming spaces.
// Empty strings are dropped.
func parseTags(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// formatNanoTimestamp formats a nanosecond Unix timestamp for display.
// Returns "HH:MM" for messages from today, "Weekday HH:MM" otherwise.
func formatNanoTimestamp(ns int64) string {
	if ns <= 0 {
		return ""
	}
	t := time.Unix(0, ns).UTC()
	now := time.Now().UTC()
	if t.Year() == now.Year() && t.Month() == now.Month() && t.Day() == now.Day() {
		return fmt.Sprintf("%02d:%02d", t.Hour(), t.Minute())
	}
	return fmt.Sprintf("%s %02d:%02d", t.Weekday().String()[:3], t.Hour(), t.Minute())
}

// sortStrings sorts a string slice in-place (insertion sort — short slices).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		key := s[i]
		j := i - 1
		for j >= 0 && s[j] > key {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = key
	}
}

// joinTags formats a tag slice as a comma-separated string for use in URLs.
func joinTags(tags []string) string {
	return strings.Join(tags, ",")
}

// tagActive returns true when tag is in the active set.
func tagActive(tag string, active []string) bool {
	for _, a := range active {
		if a == tag {
			return true
		}
	}
	return false
}

// toggleTag returns the new active set when clicking a tag chip.
// If tag is active, removes it; otherwise adds it (OR logic).
func toggleTag(tag string, active []string) string {
	result := make([]string, 0, len(active))
	found := false
	for _, a := range active {
		if a == tag {
			found = true
			continue
		}
		result = append(result, a)
	}
	if !found {
		result = append(result, tag)
	}
	return joinTags(result)
}

// templateFuncs returns the template.FuncMap used by the detail templates.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"joinTags":  joinTags,
		"tagActive": tagActive,
		"toggleTag": toggleTag,
	}
}
