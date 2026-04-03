package main

import (
	"fmt"
	"sort"
	"sync"
	"testing"
)

func TestOperatorSessionIndex_RegisterAndLookup(t *testing.T) {
	idx := newOperatorSessionIndex()

	idx.Register("account-a", "tok-1")
	idx.Register("account-a", "tok-2")
	idx.Register("account-b", "tok-3")

	if acct, ok := idx.AccountForToken("tok-1"); !ok || acct != "account-a" {
		t.Fatalf("tok-1: want (account-a, true), got (%q, %v)", acct, ok)
	}
	if acct, ok := idx.AccountForToken("tok-2"); !ok || acct != "account-a" {
		t.Fatalf("tok-2: want (account-a, true), got (%q, %v)", acct, ok)
	}
	if acct, ok := idx.AccountForToken("tok-3"); !ok || acct != "account-b" {
		t.Fatalf("tok-3: want (account-b, true), got (%q, %v)", acct, ok)
	}
	if _, ok := idx.AccountForToken("tok-unknown"); ok {
		t.Fatal("unknown token should not be found")
	}
}

func TestOperatorSessionIndex_BulkRevoke(t *testing.T) {
	idx := newOperatorSessionIndex()

	idx.Register("account-a", "tok-1")
	idx.Register("account-a", "tok-2")
	idx.Register("account-a", "tok-3")
	idx.Register("account-b", "tok-4")

	revoked := idx.RevokeAccount("account-a")
	sort.Strings(revoked)

	if len(revoked) != 3 {
		t.Fatalf("expected 3 revoked tokens, got %d: %v", len(revoked), revoked)
	}
	want := []string{"tok-1", "tok-2", "tok-3"}
	for i, w := range want {
		if revoked[i] != w {
			t.Fatalf("revoked[%d]: want %q, got %q", i, w, revoked[i])
		}
	}

	// Forward index cleared.
	if tokens := idx.TokensForAccount("account-a"); len(tokens) != 0 {
		t.Fatalf("after revoke, account-a should have 0 tokens, got %d", len(tokens))
	}

	// Reverse index cleared.
	for _, tok := range []string{"tok-1", "tok-2", "tok-3"} {
		if _, ok := idx.AccountForToken(tok); ok {
			t.Fatalf("after revoke, %q should not be findable", tok)
		}
	}

	// Unrelated account untouched.
	if acct, ok := idx.AccountForToken("tok-4"); !ok || acct != "account-b" {
		t.Fatalf("tok-4 should still belong to account-b, got (%q, %v)", acct, ok)
	}
}

func TestOperatorSessionIndex_TokensForAccountEmpty(t *testing.T) {
	idx := newOperatorSessionIndex()

	tokens := idx.TokensForAccount("no-such-account")
	if tokens == nil {
		t.Fatal("TokensForAccount must return non-nil slice for unknown account")
	}
	if len(tokens) != 0 {
		t.Fatalf("expected empty slice, got %v", tokens)
	}

	// Also check after full revocation.
	idx.Register("acct", "tok-1")
	idx.RevokeAccount("acct")
	tokens = idx.TokensForAccount("acct")
	if tokens == nil {
		t.Fatal("TokensForAccount must return non-nil slice after revocation")
	}
	if len(tokens) != 0 {
		t.Fatalf("expected empty slice after revoke, got %v", tokens)
	}
}

func TestOperatorSessionIndex_RemoveSingleToken(t *testing.T) {
	idx := newOperatorSessionIndex()

	idx.Register("account-a", "tok-1")
	idx.Register("account-a", "tok-2")

	idx.Remove("tok-1")

	// tok-1 gone from reverse map.
	if _, ok := idx.AccountForToken("tok-1"); ok {
		t.Fatal("tok-1 should be removed from reverse map")
	}

	// tok-2 still present.
	if acct, ok := idx.AccountForToken("tok-2"); !ok || acct != "account-a" {
		t.Fatalf("tok-2 should still be registered, got (%q, %v)", acct, ok)
	}

	// Forward map reflects removal.
	tokens := idx.TokensForAccount("account-a")
	if len(tokens) != 1 || tokens[0] != "tok-2" {
		t.Fatalf("account-a should have only tok-2, got %v", tokens)
	}

	// Remove last token — account entry cleaned up.
	idx.Remove("tok-2")
	tokens = idx.TokensForAccount("account-a")
	if len(tokens) != 0 {
		t.Fatalf("account-a should have no tokens after removing last one, got %v", tokens)
	}
}

func TestOperatorSessionIndex_RemoveUnknownToken(t *testing.T) {
	idx := newOperatorSessionIndex()
	// Should not panic or error.
	idx.Remove("tok-nonexistent")
}

func TestOperatorSessionIndex_RevokeEmptyAccount(t *testing.T) {
	idx := newOperatorSessionIndex()

	revoked := idx.RevokeAccount("no-such-account")
	if revoked == nil {
		t.Fatal("RevokeAccount must return non-nil slice for unknown account")
	}
	if len(revoked) != 0 {
		t.Fatalf("expected empty revoke list, got %v", revoked)
	}
}

func TestOperatorSessionIndex_ReregisterTokenDifferentAccount(t *testing.T) {
	idx := newOperatorSessionIndex()

	idx.Register("account-a", "tok-1")
	// Move tok-1 to account-b.
	idx.Register("account-b", "tok-1")

	if acct, ok := idx.AccountForToken("tok-1"); !ok || acct != "account-b" {
		t.Fatalf("tok-1 should now belong to account-b, got (%q, %v)", acct, ok)
	}
	// account-a should no longer have tok-1.
	for _, t2 := range idx.TokensForAccount("account-a") {
		if t2 == "tok-1" {
			t.Fatal("tok-1 should have been removed from account-a")
		}
	}
}

func TestOperatorSessionIndex_ConcurrentRegisterAndLookup(t *testing.T) {
	idx := newOperatorSessionIndex()

	const goroutines = 50
	const tokensPerGoroutine = 20

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			accountID := fmt.Sprintf("account-%d", g)
			for i := 0; i < tokensPerGoroutine; i++ {
				token := fmt.Sprintf("tok-%d-%d", g, i)
				idx.Register(accountID, token)
				if acct, ok := idx.AccountForToken(token); ok && acct != accountID {
					// Only check when found — concurrent re-registration may race.
					t.Errorf("token %q: want account %q, got %q", token, accountID, acct)
				}
			}
		}()
	}
	wg.Wait()

	// After all goroutines finish, every token must map to the correct account.
	for g := 0; g < goroutines; g++ {
		accountID := fmt.Sprintf("account-%d", g)
		for i := 0; i < tokensPerGoroutine; i++ {
			token := fmt.Sprintf("tok-%d-%d", g, i)
			if acct, ok := idx.AccountForToken(token); !ok || acct != accountID {
				t.Errorf("post-concurrent: token %q: want (%q, true), got (%q, %v)", token, accountID, acct, ok)
			}
		}
	}
}
