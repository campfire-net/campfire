package convention

import (
	"testing"
)

// TestWithIdentityResolver_Nil_FallsBackToNoop verifies that passing nil to
// WithIdentityResolver installs a NoopIdentityResolver rather than leaving a
// nil resolver that would panic at dispatch time.
func TestWithIdentityResolver_Nil_FallsBackToNoop(t *testing.T) {
	srv := &Server{
		handlers: make(map[string]HandlerFunc),
		resolver: NoopIdentityResolver{},
	}
	srv.WithIdentityResolver(nil)

	if srv.resolver == nil {
		t.Fatal("resolver must not be nil after WithIdentityResolver(nil)")
	}
	if _, ok := srv.resolver.(NoopIdentityResolver); !ok {
		t.Errorf("expected NoopIdentityResolver after nil guard, got %T", srv.resolver)
	}
}

// TestResolveIdentity_MalformedSenderHex verifies that resolveIdentity returns a
// zero IdentityInfo (MachineKey == nil, IdentityVerified == false) when the sender
// hex string is malformed, empty, or the wrong length.
func TestResolveIdentity_MalformedSenderHex(t *testing.T) {
	resolver := NoopIdentityResolver{}

	cases := []struct {
		name      string
		senderHex string
	}{
		{"empty", ""},
		{"not-hex", "not-hex"},
		{"invalid-chars", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"too-short", "deadbeef"},
		{"odd-length", "abc"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := resolveIdentity(tc.senderHex, resolver)
			if info.MachineKey != nil {
				t.Errorf("senderHex=%q: expected MachineKey==nil for malformed input, got %x", tc.senderHex, info.MachineKey)
			}
			if info.IdentityVerified {
				t.Errorf("senderHex=%q: expected IdentityVerified==false for malformed input", tc.senderHex)
			}
		})
	}
}
