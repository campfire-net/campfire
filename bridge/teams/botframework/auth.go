// Package botframework implements Azure Bot Service JWT validation and token acquisition.
package botframework

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// openIDConfigURL is the Bot Framework OpenID configuration endpoint.
	openIDConfigURL = "https://login.botframework.com/v1/.well-known/openidconfiguration"

	// issuerProduction is the expected JWT issuer for Azure Bot Service.
	issuerProduction = "https://api.botframework.com"

	// issuerEmulator is the accepted JWT issuer when running in emulator mode.
	issuerEmulator = "https://login.microsoftonline.com/"

	// jwksCacheTTL is how long JWKS keys are cached before re-fetching.
	jwksCacheTTL = 24 * time.Hour
)

// openIDConfig is the subset of the OpenID configuration we need.
type openIDConfig struct {
	JWKSURI string `json:"jwks_uri"`
}

// jwk represents a single JSON Web Key.
type jwk struct {
	Kid string   `json:"kid"`
	Kty string   `json:"kty"`
	N   string   `json:"n"`
	E   string   `json:"e"`
	X5c []string `json:"x5c"`
}

// jwkSet is the JSON Web Key Set response.
type jwkSet struct {
	Keys []jwk `json:"keys"`
}

// Validator validates Azure Bot Service JWTs.
type Validator struct {
	appID       string
	emulator    bool
	httpClient  *http.Client

	mu         sync.RWMutex
	keys       map[string]*rsa.PublicKey
	keysFetched time.Time
	jwksURI    string
}

// NewValidator creates a Validator for the given Bot app ID.
// If emulator is true, the issuer check is relaxed to accept the emulator issuer.
func NewValidator(appID string, emulator bool) *Validator {
	return &Validator{
		appID:      appID,
		emulator:   emulator,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		keys:       make(map[string]*rsa.PublicKey),
	}
}

// newValidatorWithClient creates a Validator with a custom HTTP client (for testing).
func newValidatorWithClient(appID string, emulator bool, client *http.Client) *Validator {
	return &Validator{
		appID:      appID,
		emulator:   emulator,
		httpClient: client,
		keys:       make(map[string]*rsa.PublicKey),
	}
}

// ValidateToken parses and validates a Bearer token from the Authorization header value.
// It returns an error if the token is invalid, expired, or has wrong issuer/audience.
func (v *Validator) ValidateToken(ctx context.Context, tokenString string) error {
	// Parse without verification first to extract kid from header.
	unverified, _, err := jwt.NewParser().ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return fmt.Errorf("parsing token header: %w", err)
	}

	kid, ok := unverified.Header["kid"].(string)
	if !ok || kid == "" {
		return fmt.Errorf("token missing kid header")
	}

	// Look up the public key, fetching JWKS if needed.
	key, err := v.getKey(ctx, kid)
	if err != nil {
		return fmt.Errorf("fetching signing key: %w", err)
	}

	// Now fully parse and validate the token.
	_, err = jwt.NewParser(
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithAudience(v.appID),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	).Parse(tokenString, func(t *jwt.Token) (any, error) {
		return key, nil
	})
	if err != nil {
		return fmt.Errorf("token validation failed: %w", err)
	}

	// Validate issuer manually so we can apply emulator relaxation.
	claims, ok := unverified.Claims.(jwt.MapClaims)
	if !ok {
		return fmt.Errorf("unexpected claims type")
	}
	iss, _ := claims["iss"].(string)
	if err := v.checkIssuer(iss); err != nil {
		return err
	}

	return nil
}

// checkIssuer validates the issuer claim against the expected value(s).
func (v *Validator) checkIssuer(iss string) error {
	if iss == issuerProduction {
		return nil
	}
	if v.emulator {
		// The emulator issues tokens with the AAD issuer prefix.
		if len(iss) >= len(issuerEmulator) && iss[:len(issuerEmulator)] == issuerEmulator {
			return nil
		}
	}
	return fmt.Errorf("invalid issuer %q", iss)
}

// getKey returns the RSA public key for the given kid, re-fetching JWKS if necessary.
func (v *Validator) getKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	// Fast path: key is in cache.
	v.mu.RLock()
	key, ok := v.keys[kid]
	cacheAge := time.Since(v.keysFetched)
	v.mu.RUnlock()

	if ok && cacheAge < jwksCacheTTL {
		return key, nil
	}

	// Slow path: fetch JWKS.
	if err := v.refreshJWKS(ctx); err != nil {
		return nil, err
	}

	v.mu.RLock()
	key, ok = v.keys[kid]
	v.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown key id %q", kid)
	}
	return key, nil
}

// refreshJWKS fetches the JWKS from the Bot Framework OpenID configuration.
func (v *Validator) refreshJWKS(ctx context.Context) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	// Double-check: another goroutine may have refreshed while we waited for the lock.
	if time.Since(v.keysFetched) < jwksCacheTTL && len(v.keys) > 0 {
		return nil
	}

	// Resolve JWKS URI if we don't have it yet.
	if v.jwksURI == "" {
		uri, err := v.fetchJWKSURI(ctx)
		if err != nil {
			return err
		}
		v.jwksURI = uri
	}

	keys, err := v.fetchJWKS(ctx, v.jwksURI)
	if err != nil {
		return err
	}

	v.keys = keys
	v.keysFetched = time.Now()
	return nil
}

// fetchJWKSURI retrieves the jwks_uri from the Bot Framework OpenID configuration.
func (v *Validator) fetchJWKSURI(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openIDConfigURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching openid config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openid config returned %d", resp.StatusCode)
	}

	var cfg openIDConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return "", fmt.Errorf("decoding openid config: %w", err)
	}
	if cfg.JWKSURI == "" {
		return "", fmt.Errorf("openid config missing jwks_uri")
	}
	return cfg.JWKSURI, nil
}

// fetchJWKS downloads and parses the JWKS at the given URI.
func (v *Validator) fetchJWKS(ctx context.Context, uri string) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching jwks: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading jwks response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks endpoint returned %d", resp.StatusCode)
	}

	var ks jwkSet
	if err := json.Unmarshal(body, &ks); err != nil {
		return nil, fmt.Errorf("parsing jwks: %w", err)
	}

	result := make(map[string]*rsa.PublicKey, len(ks.Keys))
	for _, k := range ks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := jwkToRSA(k)
		if err != nil {
			// Skip malformed keys; don't abort.
			continue
		}
		result[k.Kid] = pub
	}
	return result, nil
}

// jwkToRSA converts a JWK to an *rsa.PublicKey using the n and e fields.
func jwkToRSA(k jwk) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("decoding n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("decoding e: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)

	if !e.IsInt64() {
		return nil, fmt.Errorf("exponent too large")
	}

	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}

// injectJWKSURI overrides the JWKS URI for testing.
func (v *Validator) injectJWKSURI(uri string) {
	v.mu.Lock()
	v.jwksURI = uri
	v.mu.Unlock()
}

// injectKey injects a pre-built key directly into the cache for testing.
func (v *Validator) injectKey(kid string, key *rsa.PublicKey) {
	v.mu.Lock()
	v.keys[kid] = key
	v.keysFetched = time.Now()
	v.mu.Unlock()
}

// InjectTestKey injects an RSA public key into the Validator's key cache for
// use in tests outside this package. Do not call from production code.
func (v *Validator) InjectTestKey(kid string, key *rsa.PublicKey) {
	v.injectKey(kid, key)
}
