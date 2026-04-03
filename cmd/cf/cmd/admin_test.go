package cmd

// admin_test.go — unit tests for cf admin create-operator.
//
// Tests use an httptest server to mock the Forge API, exercising:
//   - Successful operator account creation and key generation
//   - Output format (account ID + key shown once)
//   - Error: missing admin key
//   - Error: Forge API failure on account creation
//   - Error: Forge API failure on key creation
//   - Error: empty key plaintext returned by Forge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockForgeServer creates an httptest.Server that handles the Forge account/key endpoints.
// accountHandler is called for POST /v1/accounts; keyHandler is called for POST /v1/keys.
func mockForgeServer(t *testing.T, accountHandler, keyHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/v1/accounts", accountHandler)
	mux.Handle("/v1/keys", keyHandler)
	return httptest.NewServer(mux)
}

// runAdminCreateOperator executes `cf admin create-operator` with the given extra args
// and returns (stdout, error).
// It resets cobra flag state before each call to prevent cross-test leakage.
func runAdminCreateOperator(t *testing.T, extraArgs ...string) (string, error) {
	t.Helper()

	// Reset flag values to defaults before each run so tests don't bleed state.
	adminCreateOperatorCmd.Flags().Set("name", "")           //nolint:errcheck
	adminCreateOperatorCmd.Flags().Set("forge-endpoint", "") //nolint:errcheck
	adminCreateOperatorCmd.Flags().Set("admin-key", "")      //nolint:errcheck

	// Capture stdout via cobra's OutOrStdout.
	var out bytes.Buffer
	adminCreateOperatorCmd.SetOut(&out)

	args := append([]string{"admin", "create-operator"}, extraArgs...)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()

	// Reset output writer so subsequent tests don't inherit it.
	adminCreateOperatorCmd.SetOut(nil)
	return out.String(), err
}

// TestAdminCreateOperator_Success verifies successful account creation and key output.
func TestAdminCreateOperator_Success(t *testing.T) {
	accountCalled := false
	keyCalled := false

	srv := mockForgeServer(t,
		// POST /v1/accounts
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			accountCalled = true
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
				"account_id": "acct-test-123",
				"name":       "test-operator",
				"created_at": "2026-04-01T00:00:00Z",
			})
		}),
		// POST /v1/keys
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			keyCalled = true
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
				"key":        "forge-tk-abc123xyz",
				"account_id": "acct-test-123",
				"role":       "tenant",
				"created_at": "2026-04-01T00:00:00Z",
			})
		}),
	)
	defer srv.Close()

	out, err := runAdminCreateOperator(t,
		"--forge-endpoint", srv.URL,
		"--admin-key", "forge-sk-test",
		"--name", "test-operator",
	)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if !accountCalled {
		t.Error("expected POST /v1/accounts to be called")
	}
	if !keyCalled {
		t.Error("expected POST /v1/keys to be called")
	}

	// Output must contain both pieces.
	if !strings.Contains(out, "acct-test-123") {
		t.Errorf("output missing account ID; got: %q", out)
	}
	if !strings.Contains(out, "forge-tk-abc123xyz") {
		t.Errorf("output missing API key; got: %q", out)
	}
	// Key must be labeled as shown-once.
	if !strings.Contains(out, "shown once") {
		t.Errorf("output missing 'shown once' label; got: %q", out)
	}
}

// TestAdminCreateOperator_MissingAdminKey verifies error when no admin key is provided.
func TestAdminCreateOperator_MissingAdminKey(t *testing.T) {
	// Ensure env var is not set.
	t.Setenv("FORGE_ADMIN_KEY", "")

	_, err := runAdminCreateOperator(t, "--forge-endpoint", "http://localhost:9999")
	if err == nil {
		t.Fatal("expected error for missing admin key, got nil")
	}
	if !strings.Contains(err.Error(), "forge admin key required") {
		t.Errorf("expected 'forge admin key required' error, got: %v", err)
	}
}

// TestAdminCreateOperator_ForgeAccountFailure verifies error propagation when Forge
// returns a 500 on account creation.
func TestAdminCreateOperator_ForgeAccountFailure(t *testing.T) {
	srv := mockForgeServer(t,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Should never be called.
			http.Error(w, "should not be called", http.StatusInternalServerError)
		}),
	)
	defer srv.Close()

	_, err := runAdminCreateOperator(t,
		"--forge-endpoint", srv.URL,
		"--admin-key", "forge-sk-test",
	)
	if err == nil {
		t.Fatal("expected error from Forge account creation failure, got nil")
	}
	if !strings.Contains(err.Error(), "creating operator account") {
		t.Errorf("expected 'creating operator account' in error, got: %v", err)
	}
}

// TestAdminCreateOperator_ForgeKeyFailure verifies error propagation when account
// creation succeeds but key creation fails.
func TestAdminCreateOperator_ForgeKeyFailure(t *testing.T) {
	srv := mockForgeServer(t,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
				"account_id": "acct-test-456",
				"name":       "test-op",
			})
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "key creation not supported", http.StatusBadRequest)
		}),
	)
	defer srv.Close()

	_, err := runAdminCreateOperator(t,
		"--forge-endpoint", srv.URL,
		"--admin-key", "forge-sk-test",
	)
	if err == nil {
		t.Fatal("expected error from Forge key creation failure, got nil")
	}
	if !strings.Contains(err.Error(), "creating tenant API key") {
		t.Errorf("expected 'creating tenant API key' in error, got: %v", err)
	}
}

// TestAdminCreateOperator_EmptyKeyPlaintext verifies error when Forge returns a key
// with an empty plaintext (not yet implemented on the Forge instance).
func TestAdminCreateOperator_EmptyKeyPlaintext(t *testing.T) {
	srv := mockForgeServer(t,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
				"account_id": "acct-test-789",
			})
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Return a key with an empty plaintext.
			json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
				"key":        "", // empty — Forge not yet supporting this
				"account_id": "acct-test-789",
				"role":       "tenant",
			})
		}),
	)
	defer srv.Close()

	_, err := runAdminCreateOperator(t,
		"--forge-endpoint", srv.URL,
		"--admin-key", "forge-sk-test",
	)
	if err == nil {
		t.Fatal("expected error for empty key plaintext, got nil")
	}
	if !strings.Contains(err.Error(), "empty key plaintext") {
		t.Errorf("expected 'empty key plaintext' in error, got: %v", err)
	}
}

// TestAdminCreateOperator_EnvFallback verifies that FORGE_ENDPOINT and FORGE_ADMIN_KEY
// env vars are used when flags are not set.
func TestAdminCreateOperator_EnvFallback(t *testing.T) {
	srv := mockForgeServer(t,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
				"account_id": "acct-env-test",
			})
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
				"key":        "forge-tk-env-test",
				"account_id": "acct-env-test",
				"role":       "tenant",
			})
		}),
	)
	defer srv.Close()

	t.Setenv("FORGE_ENDPOINT", srv.URL)
	t.Setenv("FORGE_ADMIN_KEY", "forge-sk-env-key")

	out, err := runAdminCreateOperator(t) // no flags
	if err != nil {
		t.Fatalf("expected success using env vars, got error: %v", err)
	}
	if !strings.Contains(out, "acct-env-test") {
		t.Errorf("output missing account ID; got: %q", out)
	}
	if !strings.Contains(out, "forge-tk-env-test") {
		t.Errorf("output missing API key; got: %q", out)
	}
}
