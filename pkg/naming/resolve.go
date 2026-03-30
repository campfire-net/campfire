package naming

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/durability"
)

// DefaultResolutionTimeout is the total timeout for resolving an entire name.
const DefaultResolutionTimeout = 10 * time.Second

// DefaultCompletionTimeout is the timeout for tab completion resolution.
const DefaultCompletionTimeout = 5 * time.Second

// MaxTTL is the maximum cacheable TTL (24 hours).
const MaxTTL = 86400

// DefaultTTL is the default TTL when none is provided (1 hour).
const DefaultTTL = 3600

// MaxRegistrationsPerDay is the rate limit for name registrations.
const MaxRegistrationsPerDay = 5

// ResolveRequest is a naming:resolve query payload.
type ResolveRequest struct {
	Name string `json:"name"`
}

// ResolveResponse is a naming:resolve fulfillment payload.
type ResolveResponse struct {
	Name              string `json:"name"`
	CampfireID        string `json:"campfire_id"`
	RegistrationMsgID string `json:"registration_msg_id,omitempty"`
	Description       string `json:"description,omitempty"`
	TTL               int    `json:"ttl,omitempty"`
}

// ListRequest is a naming:resolve-list query payload.
type ListRequest struct {
	Prefix string `json:"prefix"`
	Type   string `json:"type,omitempty"` // "api" to list endpoints
}

// ListEntry is one entry in a naming:resolve-list response.
type ListEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ListResponse is a naming:resolve-list fulfillment payload.
type ListResponse struct {
	Names []ListEntry `json:"names"`
}

// APIDeclaration is a naming:api message payload.
type APIDeclaration struct {
	Endpoint          string    `json:"endpoint"`
	Description       string    `json:"description,omitempty"`
	Args              []APIArg  `json:"args,omitempty"`
	ResultTags        []string  `json:"result_tags,omitempty"`
	ResultDescription string    `json:"result_description,omitempty"`
	Predicate         string    `json:"predicate,omitempty"`
	Sort              string    `json:"sort,omitempty"`
}

// APIArg is an argument definition in an API declaration.
type APIArg struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // string, integer, duration, boolean, key, campfire
	Description string `json:"description,omitempty"`
	Default     any    `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// InvokeRequest is a naming:api-invoke future payload.
type InvokeRequest struct {
	Endpoint string         `json:"endpoint"`
	Args     map[string]any `json:"args,omitempty"`
}

// InvokeResponse is a naming:api-invoke fulfillment payload.
type InvokeResponse struct {
	Endpoint string `json:"endpoint"`
	Results  []any  `json:"results,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Transport is the interface the resolver uses to send futures and await fulfillment.
// This decouples the naming library from the campfire transport layer.
type Transport interface {
	// Resolve sends a naming:resolve future to the given campfire and returns the response.
	Resolve(ctx context.Context, campfireID string, name string) (*ResolveResponse, error)

	// ListChildren sends a naming:resolve-list future to the given campfire.
	ListChildren(ctx context.Context, campfireID string, prefix string) (*ListResponse, error)

	// ListAPI reads naming:api messages from the given campfire.
	ListAPI(ctx context.Context, campfireID string) ([]APIDeclaration, error)

	// Invoke sends a naming:api-invoke future to the given campfire.
	Invoke(ctx context.Context, campfireID string, req *InvokeRequest) (*InvokeResponse, error)
}

// cacheEntry holds a cached name→campfire mapping.
type cacheEntry struct {
	CampfireID        string
	RegistrationMsgID string
	ExpiresAt         time.Time
}

// Resolver performs cf:// name resolution with caching and TOFU pinning.
type Resolver struct {
	transport Transport
	rootID    string // root registry campfire ID

	// AutoJoinFunc is called before reading from a campfire during the
	// hierarchical walk. If non-nil, it is invoked with each campfire ID
	// the resolver needs to read from (except the root, which the caller
	// must already have joined). If the function returns ErrInviteOnly,
	// the resolver propagates it. Other errors are wrapped and returned.
	AutoJoinFunc func(campfireID string) error

	mu              sync.RWMutex
	cache           map[string]*cacheEntry  // key: "parentID/name"
	pins            map[string]string       // key: full dotted name, value: pinned campfire ID (TOFU)
	durabilityHints map[string]string       // campfire ID → max-ttl value (from durability convention)
}

// NewResolver creates a new resolver with the given transport and root registry ID.
func NewResolver(transport Transport, rootRegistryID string) *Resolver {
	return &Resolver{
		transport:       transport,
		rootID:          rootRegistryID,
		cache:           make(map[string]*cacheEntry),
		pins:            make(map[string]string),
		durabilityHints: make(map[string]string),
	}
}

// ResolveResult is the result of a full URI resolution.
type ResolveResult struct {
	// CampfireID is the resolved campfire ID (hex-encoded public key).
	CampfireID string
	// Path is the resource path (if any) from the URI.
	Path string
	// Args are the query parameters (if any) from the URI.
	Args map[string]string
}

// ResolveURI resolves a cf:// URI to a campfire ID, optionally with path and args.
func (r *Resolver) ResolveURI(ctx context.Context, uri string) (*ResolveResult, error) {
	parsed, err := ParseURI(uri)
	if err != nil {
		return nil, fmt.Errorf("parse URI: %w", err)
	}
	return r.ResolveURIParsed(ctx, parsed)
}

// ResolveURIParsed resolves a pre-parsed URI.
func (r *Resolver) ResolveURIParsed(ctx context.Context, u *URI) (*ResolveResult, error) {
	switch u.Kind {
	case URIKindDirect:
		return &ResolveResult{
			CampfireID: u.CampfireID,
			Path:       u.Path,
			Args:       u.Args(),
		}, nil
	case URIKindAlias:
		return nil, fmt.Errorf("alias URI cf://~%s must be resolved locally via alias store before network lookup", u.Alias)
	default:
		campfireID, err := r.ResolveName(ctx, u.Segments)
		if err != nil {
			return nil, err
		}
		return &ResolveResult{
			CampfireID: campfireID,
			Path:       u.Path,
			Args:       u.Args(),
		}, nil
	}
}

// ResolveName walks the name tree segment by segment and returns the final campfire ID.
func (r *Resolver) ResolveName(ctx context.Context, segments []string) (string, error) {
	if len(segments) == 0 {
		return "", fmt.Errorf("empty name")
	}
	if len(segments) > MaxDepth {
		return "", fmt.Errorf("name exceeds maximum depth of %d segments", MaxDepth)
	}

	// Apply overall resolution timeout
	ctx, cancel := context.WithTimeout(ctx, DefaultResolutionTimeout)
	defer cancel()

	currentID := r.rootID
	visited := map[string]bool{currentID: true}

	for i, seg := range segments {
		resolved, err := r.resolveSegment(ctx, currentID, seg)
		if err != nil {
			return "", fmt.Errorf("resolving segment %q (level %d): %w", seg, i+1, err)
		}

		// Circular resolution detection
		if visited[resolved] {
			return "", fmt.Errorf("circular resolution detected at segment %q (campfire %s already visited)", seg, resolved[:12])
		}
		visited[resolved] = true

		// TOFU check
		fullName := strings.Join(segments[:i+1], ".")
		if err := r.checkTOFU(fullName, resolved); err != nil {
			return "", err
		}

		currentID = resolved
	}

	return currentID, nil
}

// resolveSegment resolves a single name segment within a parent campfire.
// Uses cache if available and not expired.
func (r *Resolver) resolveSegment(ctx context.Context, parentID, name string) (string, error) {
	cacheKey := parentID + "/" + name

	// Check cache
	r.mu.RLock()
	entry, ok := r.cache[cacheKey]
	r.mu.RUnlock()

	if ok && time.Now().Before(entry.ExpiresAt) {
		return entry.CampfireID, nil
	}

	// Auto-join if needed before reading.
	if r.AutoJoinFunc != nil {
		if err := r.AutoJoinFunc(parentID); err != nil {
			return "", fmt.Errorf("auto-join campfire %s: %w", parentID[:12], err)
		}
	}

	// Cache miss or expired — resolve via transport (direct-read)
	resp, err := r.transport.Resolve(ctx, parentID, name)
	if err != nil {
		// If we have a stale cache entry and the transport fails, invalidate it
		if ok {
			r.mu.Lock()
			delete(r.cache, cacheKey)
			r.mu.Unlock()
		}
		return "", err
	}

	// Enforce max TTL, adjusted by durability hints if present.
	defaultTTL := time.Duration(DefaultTTL) * time.Second
	ttlSeconds := resp.TTL
	if ttlSeconds <= 0 {
		ttlSeconds = DefaultTTL
	}
	if ttlSeconds > MaxTTL {
		ttlSeconds = MaxTTL
	}

	// If the resolved campfire has durability metadata, adjust cache TTL
	// per Campfire Durability Convention v0.1 §10.4.
	cacheDur := time.Duration(ttlSeconds) * time.Second
	if maxTTL, ok := r.durabilityHints[resp.CampfireID]; ok {
		cacheDur = durability.URICacheTTL(maxTTL, defaultTTL)
		// Still respect the per-response TTL as an upper bound.
		respDur := time.Duration(ttlSeconds) * time.Second
		if respDur < cacheDur {
			cacheDur = respDur
		}
	}

	// Cache the result (unless TTL is 0 meaning do-not-cache)
	if resp.TTL != 0 {
		r.mu.Lock()
		r.cache[cacheKey] = &cacheEntry{
			CampfireID:        resp.CampfireID,
			RegistrationMsgID: resp.RegistrationMsgID,
			ExpiresAt:         time.Now().Add(cacheDur),
		}
		r.mu.Unlock()
	}

	return resp.CampfireID, nil
}

// checkTOFU checks the TOFU pin for a name. If the name has been previously
// resolved, the new campfire ID must match the pinned value.
func (r *Resolver) checkTOFU(name, campfireID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	pinned, ok := r.pins[name]
	if !ok {
		// First resolution — pin it
		r.pins[name] = campfireID
		return nil
	}

	if pinned != campfireID {
		return &TOFUViolation{
			Name:       name,
			PinnedID:   pinned,
			ResolvedID: campfireID,
		}
	}
	return nil
}

// TOFUViolation is returned when a resolved campfire ID doesn't match the pinned value.
type TOFUViolation struct {
	Name       string
	PinnedID   string
	ResolvedID string
}

func (e *TOFUViolation) Error() string {
	return fmt.Sprintf("TOFU violation for %q: pinned %s, resolved %s", e.Name, e.PinnedID[:12], e.ResolvedID[:12])
}

// SetDurabilityHint records the max-ttl durability value for a campfire ID.
// When this campfire is resolved, the cache TTL is adjusted using
// durability.URICacheTTL per Campfire Durability Convention v0.1 §10.4.
// Pass an empty maxTTL to remove the hint.
func (r *Resolver) SetDurabilityHint(campfireID, maxTTL string) {
	r.mu.Lock()
	if maxTTL == "" {
		delete(r.durabilityHints, campfireID)
	} else {
		r.durabilityHints[campfireID] = maxTTL
	}
	r.mu.Unlock()
}

// InvalidateCache removes a specific name mapping from the cache.
func (r *Resolver) InvalidateCache(parentID, name string) {
	r.mu.Lock()
	delete(r.cache, parentID+"/"+name)
	r.mu.Unlock()
}

// ClearTOFUPin removes a TOFU pin (use when intentionally accepting a name transfer).
func (r *Resolver) ClearTOFUPin(name string) {
	r.mu.Lock()
	delete(r.pins, name)
	r.mu.Unlock()
}

// ListChildren queries a campfire for its registered child names.
func (r *Resolver) ListChildren(ctx context.Context, campfireID, prefix string) ([]ListEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultCompletionTimeout)
	defer cancel()

	resp, err := r.transport.ListChildren(ctx, campfireID, prefix)
	if err != nil {
		return nil, err
	}

	// Sanitize descriptions
	for i := range resp.Names {
		resp.Names[i].Description = SanitizeDescription(resp.Names[i].Description)
	}

	return resp.Names, nil
}

// ListAPI queries a campfire for its declared API endpoints.
func (r *Resolver) ListAPI(ctx context.Context, campfireID string) ([]APIDeclaration, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultCompletionTimeout)
	defer cancel()

	decls, err := r.transport.ListAPI(ctx, campfireID)
	if err != nil {
		return nil, err
	}

	// Sanitize descriptions
	for i := range decls {
		decls[i].Description = SanitizeDescription(decls[i].Description)
	}

	return decls, nil
}

// Invoke resolves a URI and invokes the future at the resolved campfire.
func (r *Resolver) Invoke(ctx context.Context, uri string) (*InvokeResponse, error) {
	result, err := r.ResolveURI(ctx, uri)
	if err != nil {
		return nil, err
	}
	if result.Path == "" {
		return nil, fmt.Errorf("URI has no path component — nothing to invoke")
	}

	// Convert string args to any
	args := make(map[string]any, len(result.Args))
	for k, v := range result.Args {
		args[k] = v
	}

	return r.transport.Invoke(ctx, result.CampfireID, &InvokeRequest{
		Endpoint: result.Path,
		Args:     args,
	})
}

// ResolveOrPassthrough resolves a cf:// URI to a campfire ID, or returns
// the input unchanged if it's already a hex campfire ID (not a cf:// URI).
func (r *Resolver) ResolveOrPassthrough(ctx context.Context, input string) (string, error) {
	if !IsCampfireURI(input) {
		return input, nil
	}
	result, err := r.ResolveURI(ctx, input)
	if err != nil {
		return "", err
	}
	return result.CampfireID, nil
}

// MarshalResolveRequest creates the JSON payload for a naming:resolve future.
func MarshalResolveRequest(name string) ([]byte, error) {
	return json.Marshal(&ResolveRequest{Name: name})
}

// MarshalListRequest creates the JSON payload for a naming:resolve-list future.
func MarshalListRequest(prefix string) ([]byte, error) {
	return json.Marshal(&ListRequest{Prefix: prefix})
}

// MarshalInvokeRequest creates the JSON payload for a naming:api-invoke future.
func MarshalInvokeRequest(endpoint string, args map[string]any) ([]byte, error) {
	return json.Marshal(&InvokeRequest{Endpoint: endpoint, Args: args})
}

