package main

import "sync"

// operatorSessionIndex is a thread-safe bidirectional map between operator
// account IDs and session tokens. It is a pure association table — it does
// not touch TokenRegistry directly. Callers receive the revoked token list
// and pass it to TokenRegistry.revoke themselves.
//
// Invariants:
//   - Every entry in tokenToAccount has a corresponding entry in accountToTokens.
//   - Every token in accountToTokens has a corresponding entry in tokenToAccount.
type operatorSessionIndex struct {
	mu              sync.RWMutex
	accountToTokens map[string]map[string]struct{} // accountID → set of tokens
	tokenToAccount  map[string]string               // token → accountID
}

// newOperatorSessionIndex creates an empty operatorSessionIndex.
func newOperatorSessionIndex() *operatorSessionIndex {
	return &operatorSessionIndex{
		accountToTokens: make(map[string]map[string]struct{}),
		tokenToAccount:  make(map[string]string),
	}
}

// Register records the association between accountID and token.
// Re-registering the same token with the same account is idempotent.
// Re-registering a token with a different account replaces the old association.
func (idx *operatorSessionIndex) Register(accountID, token string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// If this token was already registered under a different account, remove it
	// from the old account's set first.
	if prev, ok := idx.tokenToAccount[token]; ok && prev != accountID {
		delete(idx.accountToTokens[prev], token)
		if len(idx.accountToTokens[prev]) == 0 {
			delete(idx.accountToTokens, prev)
		}
	}

	idx.tokenToAccount[token] = accountID
	if idx.accountToTokens[accountID] == nil {
		idx.accountToTokens[accountID] = make(map[string]struct{})
	}
	idx.accountToTokens[accountID][token] = struct{}{}
}

// TokensForAccount returns all tokens currently registered for accountID.
// Returns an empty (non-nil) slice when the account has no tokens.
func (idx *operatorSessionIndex) TokensForAccount(accountID string) []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	set := idx.accountToTokens[accountID]
	result := make([]string, 0, len(set))
	for t := range set {
		result = append(result, t)
	}
	return result
}

// AccountForToken returns the accountID associated with token.
// ok is false when the token is not registered.
func (idx *operatorSessionIndex) AccountForToken(token string) (accountID string, ok bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	accountID, ok = idx.tokenToAccount[token]
	return
}

// RevokeAccount removes all entries for accountID from both directions and
// returns the list of tokens that were revoked. The caller is responsible for
// passing the returned tokens to TokenRegistry to complete revocation.
// Returns an empty (non-nil) slice when the account has no tokens.
func (idx *operatorSessionIndex) RevokeAccount(accountID string) []string {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	set := idx.accountToTokens[accountID]
	revoked := make([]string, 0, len(set))
	for t := range set {
		delete(idx.tokenToAccount, t)
		revoked = append(revoked, t)
	}
	delete(idx.accountToTokens, accountID)
	return revoked
}

// Remove deletes the association for a single token. It is called when a
// session expires naturally (as opposed to bulk account revocation).
func (idx *operatorSessionIndex) Remove(token string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	accountID, ok := idx.tokenToAccount[token]
	if !ok {
		return
	}
	delete(idx.tokenToAccount, token)
	delete(idx.accountToTokens[accountID], token)
	if len(idx.accountToTokens[accountID]) == 0 {
		delete(idx.accountToTokens, accountID)
	}
}
