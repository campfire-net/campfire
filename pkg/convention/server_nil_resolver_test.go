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
