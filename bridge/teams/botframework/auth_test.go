package botframework

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// testKey generates a fresh RSA-2048 key pair for use in tests.
func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test RSA key: %v", err)
	}
	return key
}

// bigIntToBase64URL encodes a big.Int as a base64url string (no padding).
func bigIntToBase64URL(n *big.Int) string {
	return base64.RawURLEncoding.EncodeToString(n.Bytes())
}

// makeJWKSServer starts an httptest.Server that serves a JWKS containing the given key.
func makeJWKSServer(t *testing.T, kid string, key *rsa.PublicKey) *httptest.Server {
	t.Helper()
	eBytes := big.NewInt(int64(key.E)).Bytes()
	jwksBody := map[string]any{
		"keys": []map[string]any{
			{
				"kid": kid,
				"kty": "RSA",
				"n":   bigIntToBase64URL(key.N),
				"e":   base64.RawURLEncoding.EncodeToString(eBytes),
			},
		},
	}
	raw, err := json.Marshal(jwksBody)
	if err != nil {
		t.Fatalf("marshalling test JWKS: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

// signToken builds and signs a JWT using the test key.
func signToken(t *testing.T, key *rsa.PrivateKey, kid, issuer, audience string, expiry time.Time) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": issuer,
		"aud": audience,
		"exp": expiry.Unix(),
		"iat": time.Now().Unix(),
		"nbf": time.Now().Unix(),
	})
	tok.Header["kid"] = kid

	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("signing test token: %v", err)
	}
	return signed
}

// --- Validator tests ---

func TestValidator_ValidToken(t *testing.T) {
	const appID = "test-app-id"
	const kid = "test-kid-1"

	key := testKey(t)
	srv := makeJWKSServer(t, kid, &key.PublicKey)

	v := NewValidator(appID, false)
	v.injectJWKSURI(srv.URL)

	token := signToken(t, key, kid, issuerProduction, appID, time.Now().Add(5*time.Minute))
	if err := v.ValidateToken(context.Background(), token); err != nil {
		t.Fatalf("expected valid token to pass, got: %v", err)
	}
}

func TestValidator_ExpiredToken(t *testing.T) {
	const appID = "test-app-id"
	const kid = "test-kid-2"

	key := testKey(t)
	v := NewValidator(appID, false)
	v.injectKey(kid, &key.PublicKey)

	token := signToken(t, key, kid, issuerProduction, appID, time.Now().Add(-1*time.Minute))
	err := v.ValidateToken(context.Background(), token)
	if err == nil {
		t.Fatal("expected expired token to fail validation")
	}
}

func TestValidator_WrongAudience(t *testing.T) {
	const appID = "test-app-id"
	const kid = "test-kid-3"

	key := testKey(t)
	v := NewValidator(appID, false)
	v.injectKey(kid, &key.PublicKey)

	token := signToken(t, key, kid, issuerProduction, "wrong-audience", time.Now().Add(5*time.Minute))
	err := v.ValidateToken(context.Background(), token)
	if err == nil {
		t.Fatal("expected wrong audience to fail validation")
	}
}

func TestValidator_WrongIssuer(t *testing.T) {
	const appID = "test-app-id"
	const kid = "test-kid-4"

	key := testKey(t)
	v := NewValidator(appID, false)
	v.injectKey(kid, &key.PublicKey)

	token := signToken(t, key, kid, "https://evil.example.com", appID, time.Now().Add(5*time.Minute))
	err := v.ValidateToken(context.Background(), token)
	if err == nil {
		t.Fatal("expected wrong issuer to fail validation")
	}
}

func TestValidator_WrongSignature(t *testing.T) {
	const appID = "test-app-id"
	const kid = "test-kid-5"

	signerKey := testKey(t)   // key used to sign
	validatorKey := testKey(t) // different key in the cache

	v := NewValidator(appID, false)
	v.injectKey(kid, &validatorKey.PublicKey) // inject a different key

	token := signToken(t, signerKey, kid, issuerProduction, appID, time.Now().Add(5*time.Minute))
	err := v.ValidateToken(context.Background(), token)
	if err == nil {
		t.Fatal("expected wrong signature to fail validation")
	}
}

func TestValidator_EmulatorIssuer_Accepted(t *testing.T) {
	const appID = "test-app-id"
	const kid = "test-kid-6"

	key := testKey(t)
	v := NewValidator(appID, true) // emulator=true
	v.injectKey(kid, &key.PublicKey)

	// Emulator issues tokens with AAD-style issuer
	emulatorIssuer := "https://login.microsoftonline.com/d6d49420-f39b-11ef-8b64-0242ac120002/v2.0"
	token := signToken(t, key, kid, emulatorIssuer, appID, time.Now().Add(5*time.Minute))
	if err := v.ValidateToken(context.Background(), token); err != nil {
		t.Fatalf("emulator issuer should be accepted in emulator mode, got: %v", err)
	}
}

func TestValidator_EmulatorIssuer_RejectedInProduction(t *testing.T) {
	const appID = "test-app-id"
	const kid = "test-kid-7"

	key := testKey(t)
	v := NewValidator(appID, false) // emulator=false
	v.injectKey(kid, &key.PublicKey)

	emulatorIssuer := "https://login.microsoftonline.com/d6d49420-f39b-11ef-8b64-0242ac120002/v2.0"
	token := signToken(t, key, kid, emulatorIssuer, appID, time.Now().Add(5*time.Minute))
	err := v.ValidateToken(context.Background(), token)
	if err == nil {
		t.Fatal("emulator issuer should be rejected in production mode")
	}
}

func TestValidator_UnknownKid_RefetchesJWKS(t *testing.T) {
	const appID = "test-app-id"
	const kid = "test-kid-new"

	key := testKey(t)
	srv := makeJWKSServer(t, kid, &key.PublicKey)

	// Create validator with a stale cache (different kid), pointing at JWKS server.
	v := NewValidator(appID, false)
	v.injectJWKSURI(srv.URL)
	// Inject an unrelated key under a different kid so the cache is populated but stale.
	otherKey := testKey(t)
	v.injectKey("old-kid", &otherKey.PublicKey)
	// Expire the cache so the unknown-kid path forces a refresh.
	v.mu.Lock()
	v.keysFetched = time.Time{} // zero = never fetched
	v.mu.Unlock()

	token := signToken(t, key, kid, issuerProduction, appID, time.Now().Add(5*time.Minute))
	if err := v.ValidateToken(context.Background(), token); err != nil {
		t.Fatalf("expected re-fetch to find new key, got: %v", err)
	}
}

func TestValidator_MalformedToken(t *testing.T) {
	v := NewValidator("any-app", false)
	err := v.ValidateToken(context.Background(), "not.a.jwt")
	if err == nil {
		t.Fatal("expected malformed token to fail")
	}
}

func TestValidator_JWKS_ParsesRSAKey(t *testing.T) {
	key := testKey(t)
	const kid = "parse-test"

	srv := makeJWKSServer(t, kid, &key.PublicKey)

	v := NewValidator("unused", false)
	v.injectJWKSURI(srv.URL)

	ctx := context.Background()
	got, err := v.getKey(ctx, kid)
	if err != nil {
		t.Fatalf("getKey: %v", err)
	}
	if got.N.Cmp(key.PublicKey.N) != 0 {
		t.Error("parsed key modulus does not match original")
	}
	if got.E != key.PublicKey.E {
		t.Error("parsed key exponent does not match original")
	}
}

// --- TokenClient tests ---

func TestTokenClient_GetToken_Success(t *testing.T) {
	const appID = "app-123"
	const appPwd = "secret"
	const tenantID = "tenant-456"
	const fakeToken = "eyJhbGciOiJSUzI1NiJ9.test.token"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing form: %v", err)
		}
		if r.FormValue("grant_type") != "client_credentials" {
			t.Errorf("expected grant_type=client_credentials, got %q", r.FormValue("grant_type"))
		}
		if r.FormValue("client_id") != appID {
			t.Errorf("expected client_id=%q, got %q", appID, r.FormValue("client_id"))
		}
		if r.FormValue("client_secret") != appPwd {
			t.Errorf("expected client_secret=%q, got %q", appPwd, r.FormValue("client_secret"))
		}
		if r.FormValue("scope") != botFrameworkScope {
			t.Errorf("expected scope=%q, got %q", botFrameworkScope, r.FormValue("scope"))
		}

		resp := tokenResponse{
			AccessToken: fakeToken,
			ExpiresIn:   3600,
			TokenType:   "Bearer",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTokenClientWithClient(appID, appPwd, tenantID, srv.Client())
	c.tokenURL = srv.URL // override to point at test server

	tok, err := c.GetToken(context.Background())
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if tok != fakeToken {
		t.Errorf("expected token %q, got %q", fakeToken, tok)
	}
}

func TestTokenClient_GetToken_Cached(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		resp := tokenResponse{
			AccessToken: "cached-token",
			ExpiresIn:   3600,
		}
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTokenClientWithClient("app", "pwd", "tenant", srv.Client())
	c.tokenURL = srv.URL

	// First call — hits server.
	tok1, err := c.GetToken(context.Background())
	if err != nil {
		t.Fatalf("first GetToken: %v", err)
	}

	// Second call — should use cache.
	tok2, err := c.GetToken(context.Background())
	if err != nil {
		t.Fatalf("second GetToken: %v", err)
	}

	if tok1 != tok2 {
		t.Errorf("expected same token from cache, got %q vs %q", tok1, tok2)
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 HTTP call, got %d", calls)
	}
}

func TestTokenClient_GetToken_RefreshesWhenExpired(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		resp := tokenResponse{
			AccessToken: "token-" + strings.Repeat("x", calls),
			ExpiresIn:   3600,
		}
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer srv.Close()

	c := newTokenClientWithClient("app", "pwd", "tenant", srv.Client())
	c.tokenURL = srv.URL

	// Pre-populate an already-expired token.
	c.cache = &cachedToken{
		accessToken: "old-token",
		expiresAt:   time.Now().Add(-1 * time.Minute),
	}

	tok, err := c.GetToken(context.Background())
	if err != nil {
		t.Fatalf("GetToken: %v", err)
	}
	if tok == "old-token" {
		t.Error("expected refresh; got stale cached token")
	}
	if calls != 1 {
		t.Errorf("expected 1 HTTP call for refresh, got %d", calls)
	}
}

func TestTokenClient_GetToken_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := newTokenClientWithClient("app", "pwd", "tenant", srv.Client())
	c.tokenURL = srv.URL

	_, err := c.GetToken(context.Background())
	if err == nil {
		t.Fatal("expected error on server 401")
	}
}
