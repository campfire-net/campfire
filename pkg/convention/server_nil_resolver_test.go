package convention

import (
	"context"
	"errors"
	"testing"

	"github.com/campfire-net/campfire/pkg/protocol"
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

// TestDispatch_MalformedSenderHex_SkipsHandler verifies that the server dispatch
// guards against nil MachineKey — a message with a malformed sender hex must not
// reach the handler, preventing potential nil pointer panics in handler code.
func TestDispatch_MalformedSenderHex_SkipsHandler(t *testing.T) {
	handlerCalled := false
	var errReceived error

	srv := &Server{
		handlers: map[string]HandlerFunc{
			"ping": func(_ context.Context, _ *Request) (*Response, error) {
				handlerCalled = true
				return nil, nil
			},
		},
		decl: &Declaration{
			Convention: "test",
			Operation:  "ping",
		},
		resolver: NoopIdentityResolver{},
		errFn: func(err error) {
			errReceived = err
		},
	}

	msg := protocol.Message{
		ID:      "msg-001",
		Sender:  "not-valid-hex",
		Payload: []byte(`{}`),
		Tags:    []string{"convention:test:ping"},
	}

	srv.dispatch(context.Background(), "cf-abc123", msg)

	if handlerCalled {
		t.Error("handler must not be called when sender hex is malformed (nil MachineKey)")
	}
	if errReceived == nil {
		t.Error("expected errFn to be called with malformed sender error")
	}
	if errReceived != nil && !errors.Is(errReceived, errReceived) {
		// Always true — just confirms err is non-nil (used for structure).
		t.Errorf("unexpected error: %v", errReceived)
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
			info := resolveIdentity(context.Background(), tc.senderHex, resolver)
			if info.MachineKey != nil {
				t.Errorf("senderHex=%q: expected MachineKey==nil for malformed input, got %x", tc.senderHex, info.MachineKey)
			}
			if info.IdentityVerified {
				t.Errorf("senderHex=%q: expected IdentityVerified==false for malformed input", tc.senderHex)
			}
		})
	}
}
