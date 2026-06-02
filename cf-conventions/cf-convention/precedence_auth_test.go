package convention

import (
	"context"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/store"
)

// These tests cover campfire-f5c: declaration PRECEDENCE (both the Supersedes path
// and the version-dedup path) must be authorized so that an operation cannot be
// silently hijacked by any writer, while a legitimate operator can still upgrade
// their own convention. The authorization mirrors the revoke rule already enforced
// in listOperations: self-upgrade (same signer) always; campfire-key override only
// in online mode; an empty sender is never authorized.

// declPayload builds a minimal valid convention:operation declaration payload.
func declPayload(conv, version, op string) []byte {
	return mustJSON(map[string]any{
		"convention":  conv,
		"version":     version,
		"operation":   op,
		"description": conv + ":" + op + "@" + version,
		"antecedents": "none",
		"signing":     "member_key",
	})
}

// supersedePayload builds a declaration that supersedes the given target message ID.
func supersedePayload(conv, version, op, supersedes string) []byte {
	return mustJSON(map[string]any{
		"convention":  conv,
		"version":     version,
		"operation":   op,
		"description": conv + ":" + op + "@" + version,
		"supersedes":  supersedes,
		"antecedents": "none",
		"signing":     "member_key",
	})
}

func opRec(id, sender string, payload []byte, ts int64) store.MessageRecord {
	return store.MessageRecord{
		ID:        id,
		Sender:    sender,
		Payload:   payload,
		Tags:      []string{ConventionOperationTag},
		Timestamp: ts,
	}
}

// soleDecl asserts exactly one declaration survived and returns it.
func soleDecl(t *testing.T, decls []*Declaration) *Declaration {
	t.Helper()
	if len(decls) != 1 {
		t.Fatalf("len(decls) = %d, want 1; got %+v", len(decls), decls)
	}
	return decls[0]
}

// TestPrecedenceAuthorized is a direct table test of the gate predicate.
func TestPrecedenceAuthorized(t *testing.T) {
	cases := []struct {
		name        string
		candidate   string
		owner       string
		campfireKey string
		want        bool
	}{
		{"self-upgrade offline", "op", "op", "", true},
		{"self-upgrade online", "op", "op", "OWNER", true},
		{"different signer offline rejected", "evil", "op", "", false},
		{"different signer online rejected", "evil", "op", "OWNER", false},
		{"owner override online", "OWNER", "op", "OWNER", true},
		{"owner key irrelevant offline", "OWNER", "op", "", false},
		{"empty candidate offline rejected", "", "op", "", false},
		{"empty candidate online rejected", "", "op", "OWNER", false},
		{"empty owner rejects non-owner", "op", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := precedenceAuthorized(c.candidate, c.owner, c.campfireKey); got != c.want {
				t.Errorf("precedenceAuthorized(%q,%q,%q) = %v, want %v",
					c.candidate, c.owner, c.campfireKey, got, c.want)
			}
		})
	}
}

// TestPrecedence_VersionDedupHijackRejected: an attacker posting a higher version
// number with a different signer cannot displace the slot owner's declaration.
func TestPrecedence_VersionDedupHijackRejected(t *testing.T) {
	mock := &mockStore{records: []store.MessageRecord{
		opRec("legit1", "op", declPayload("welcome-center", "0.1", "respond-to-greeting"), 1000),
		opRec("legit2", "op", declPayload("welcome-center", "0.2", "respond-to-greeting"), 2000),
		opRec("evil", "attacker", declPayload("welcome-center", "99.0", "respond-to-greeting"), 3000),
	}}

	decls, err := ListOperations(context.Background(), mock, "cf", "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	d := soleDecl(t, decls)
	if d.Version != "0.2" {
		t.Errorf("version = %q, want 0.2 (legit latest, attacker's 99.0 rejected)", d.Version)
	}
	if d.MessageID != "legit2" {
		t.Errorf("messageID = %q, want legit2", d.MessageID)
	}
}

// TestPrecedence_SupersedeHijackRejected: an attacker cannot replace another
// signer's declaration via a crafted Supersedes link.
func TestPrecedence_SupersedeHijackRejected(t *testing.T) {
	mock := &mockStore{records: []store.MessageRecord{
		opRec("legit", "op", declPayload("welcome-center", "0.1", "respond-to-greeting"), 1000),
		opRec("evil", "attacker", supersedePayload("welcome-center", "0.2", "respond-to-greeting", "legit"), 2000),
	}}

	decls, err := ListOperations(context.Background(), mock, "cf", "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	d := soleDecl(t, decls)
	if d.MessageID != "legit" {
		t.Errorf("messageID = %q, want legit (attacker supersede rejected, attacker decl dropped from slot)", d.MessageID)
	}
	if d.Version != "0.1" {
		t.Errorf("version = %q, want 0.1", d.Version)
	}
}

// TestPrecedence_LegitSelfUpgradeOffline: the original signer may upgrade their own
// declaration, both by version bump and by explicit supersede, in offline mode.
func TestPrecedence_LegitSelfUpgradeOffline(t *testing.T) {
	t.Run("version-bump", func(t *testing.T) {
		mock := &mockStore{records: []store.MessageRecord{
			opRec("v1", "op", declPayload("welcome-center", "0.1", "respond-to-greeting"), 1000),
			opRec("v2", "op", declPayload("welcome-center", "0.2", "respond-to-greeting"), 2000),
		}}
		decls, err := ListOperations(context.Background(), mock, "cf", "")
		if err != nil {
			t.Fatalf("ListOperations: %v", err)
		}
		if d := soleDecl(t, decls); d.Version != "0.2" {
			t.Errorf("version = %q, want 0.2", d.Version)
		}
	})
	t.Run("explicit-supersede", func(t *testing.T) {
		mock := &mockStore{records: []store.MessageRecord{
			opRec("v1", "op", declPayload("welcome-center", "0.1", "respond-to-greeting"), 1000),
			opRec("v2", "op", supersedePayload("welcome-center", "0.2", "respond-to-greeting", "v1"), 2000),
		}}
		decls, err := ListOperations(context.Background(), mock, "cf", "")
		if err != nil {
			t.Fatalf("ListOperations: %v", err)
		}
		d := soleDecl(t, decls)
		if d.Version != "0.2" || d.MessageID != "v2" {
			t.Errorf("got %s@%s, want v2@0.2", d.MessageID, d.Version)
		}
	})
}

// TestPrecedence_OwnerOverrideOnline: in online mode the campfire key may take
// precedence over another signer's declaration (forced upgrade of an abandoned op),
// via both supersede and version bump.
func TestPrecedence_OwnerOverrideOnline(t *testing.T) {
	const ownerKey = "OWNERKEY"
	t.Run("supersede", func(t *testing.T) {
		mock := &mockStore{records: []store.MessageRecord{
			opRec("memberDecl", "member", declPayload("welcome-center", "0.1", "respond-to-greeting"), 1000),
			opRec("ownerDecl", ownerKey, supersedePayload("welcome-center", "0.2", "respond-to-greeting", "memberDecl"), 2000),
		}}
		decls, err := ListOperations(context.Background(), mock, "cf", ownerKey)
		if err != nil {
			t.Fatalf("ListOperations: %v", err)
		}
		if d := soleDecl(t, decls); d.MessageID != "ownerDecl" {
			t.Errorf("messageID = %q, want ownerDecl (owner override)", d.MessageID)
		}
	})
	t.Run("version-bump", func(t *testing.T) {
		mock := &mockStore{records: []store.MessageRecord{
			opRec("memberDecl", "member", declPayload("welcome-center", "0.1", "respond-to-greeting"), 1000),
			opRec("ownerDecl", ownerKey, declPayload("welcome-center", "0.3", "respond-to-greeting"), 2000),
		}}
		decls, err := ListOperations(context.Background(), mock, "cf", ownerKey)
		if err != nil {
			t.Fatalf("ListOperations: %v", err)
		}
		if d := soleDecl(t, decls); d.Version != "0.3" || d.MessageID != "ownerDecl" {
			t.Errorf("got %s@%s, want ownerDecl@0.3 (owner override)", d.MessageID, d.Version)
		}
	})
	t.Run("non-owner still rejected online", func(t *testing.T) {
		mock := &mockStore{records: []store.MessageRecord{
			opRec("memberDecl", "member", declPayload("welcome-center", "0.1", "respond-to-greeting"), 1000),
			opRec("evil", "attacker", declPayload("welcome-center", "99.0", "respond-to-greeting"), 2000),
		}}
		decls, err := ListOperations(context.Background(), mock, "cf", ownerKey)
		if err != nil {
			t.Fatalf("ListOperations: %v", err)
		}
		if d := soleDecl(t, decls); d.MessageID != "memberDecl" {
			t.Errorf("messageID = %q, want memberDecl (attacker rejected even online)", d.MessageID)
		}
	})
}

// TestPrecedence_EmptySenderCannotHijack: an empty sender is never authorized to
// take precedence on either path. A SOLE empty-sender declaration still survives
// (the slot owner always occupies its own slot) — that is the regression guard for
// existing offline behavior.
func TestPrecedence_EmptySenderCannotHijack(t *testing.T) {
	t.Run("empty-sender-version-bump-rejected", func(t *testing.T) {
		mock := &mockStore{records: []store.MessageRecord{
			opRec("legit", "op", declPayload("welcome-center", "0.1", "respond-to-greeting"), 1000),
			opRec("evil", "", declPayload("welcome-center", "99.0", "respond-to-greeting"), 2000),
		}}
		decls, err := ListOperations(context.Background(), mock, "cf", "")
		if err != nil {
			t.Fatalf("ListOperations: %v", err)
		}
		if d := soleDecl(t, decls); d.MessageID != "legit" {
			t.Errorf("messageID = %q, want legit (empty-sender attacker rejected)", d.MessageID)
		}
	})
	t.Run("empty-sender-supersede-rejected", func(t *testing.T) {
		mock := &mockStore{records: []store.MessageRecord{
			opRec("legit", "op", declPayload("welcome-center", "0.1", "respond-to-greeting"), 1000),
			opRec("evil", "", supersedePayload("welcome-center", "0.2", "respond-to-greeting", "legit"), 2000),
		}}
		decls, err := ListOperations(context.Background(), mock, "cf", "")
		if err != nil {
			t.Fatalf("ListOperations: %v", err)
		}
		if d := soleDecl(t, decls); d.MessageID != "legit" {
			t.Errorf("messageID = %q, want legit (empty-sender supersede rejected)", d.MessageID)
		}
	})
	t.Run("sole-empty-sender-decl-survives", func(t *testing.T) {
		mock := &mockStore{records: []store.MessageRecord{
			opRec("only", "", declPayload("welcome-center", "0.1", "respond-to-greeting"), 1000),
		}}
		decls, err := ListOperations(context.Background(), mock, "cf", "")
		if err != nil {
			t.Fatalf("ListOperations: %v", err)
		}
		if d := soleDecl(t, decls); d.MessageID != "only" {
			t.Errorf("messageID = %q, want only (sole declaration must survive)", d.MessageID)
		}
	})
}

// TestPrecedence_CrossSourceRegistryHijackRejected: a declaration injected via the
// registry campfire cannot hijack an inline operation owned by another signer, but
// the original signer CAN upgrade across sources.
func TestPrecedence_CrossSourceRegistryHijackRejected(t *testing.T) {
	t.Run("registry-attacker-rejected", func(t *testing.T) {
		ms := &multiCampfireStore{stores: map[string][]store.MessageRecord{
			"inline":   {opRec("legit", "op", declPayload("welcome-center", "0.1", "respond-to-greeting"), 1000)},
			"registry": {opRec("evil", "attacker", declPayload("welcome-center", "99.0", "respond-to-greeting"), 2000)},
		}}
		decls, err := ListOperationsWithRegistry(context.Background(), ms, "inline", "", "registry")
		if err != nil {
			t.Fatalf("ListOperationsWithRegistry: %v", err)
		}
		if d := soleDecl(t, decls); d.MessageID != "legit" {
			t.Errorf("messageID = %q, want legit (cross-source attacker rejected)", d.MessageID)
		}
	})
	t.Run("registry-same-signer-upgrade-honored", func(t *testing.T) {
		ms := &multiCampfireStore{stores: map[string][]store.MessageRecord{
			"inline":   {opRec("legit", "op", declPayload("welcome-center", "0.1", "respond-to-greeting"), 1000)},
			"registry": {opRec("up", "op", supersedePayload("welcome-center", "0.2", "respond-to-greeting", "legit"), 2000)},
		}}
		decls, err := ListOperationsWithRegistry(context.Background(), ms, "inline", "", "registry")
		if err != nil {
			t.Fatalf("ListOperationsWithRegistry: %v", err)
		}
		if d := soleDecl(t, decls); d.Version != "0.2" || d.MessageID != "up" {
			t.Errorf("got %s@%s, want up@0.2 (same-signer cross-source upgrade)", d.MessageID, d.Version)
		}
	})
}

// TestPrecedence_HijackInvisibleToDispatch is the resonance assertion: after a
// rejected hijack, the SINGLE declaration that survives resolution is the legit one,
// so CLI dispatch / help (which take the first match of ListOperations) and any
// consumer all resolve the same authoritative declaration — no divergence.
func TestPrecedence_HijackInvisibleToDispatch(t *testing.T) {
	mock := &mockStore{records: []store.MessageRecord{
		opRec("legit", "op", declPayload("welcome-center", "0.3", "respond-to-greeting"), 1000),
		// Two separate attackers, both higher version, both other signers.
		opRec("evilA", "attackerA", declPayload("welcome-center", "50.0", "respond-to-greeting"), 2000),
		opRec("evilB", "attackerB", supersedePayload("welcome-center", "60.0", "respond-to-greeting", "legit"), 3000),
	}}
	decls, err := ListOperations(context.Background(), mock, "cf", "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	d := soleDecl(t, decls)
	if d.MessageID != "legit" || d.Version != "0.3" {
		t.Errorf("dispatch resolves %s@%s, want legit@0.3 (both hijacks rejected)", d.MessageID, d.Version)
	}
}
