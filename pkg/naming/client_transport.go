package naming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	discovery "github.com/campfire-net/campfire/cf-conventions/cf-discovery"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/pkg/beacon"
)

// clientTransport implements the naming.Transport interface using a
// protocol.Client. Resolve and ListChildren use direct-read (reading tagged
// registration messages) — no futures, no server process needed. ListAPI also
// uses direct-read. Only Invoke still uses futures (it is RPC, not data
// lookup).
type clientTransport struct {
	client *protocol.Client
}

// ResolverClientOptions configures the resolver created by NewResolverFromClient.
type ResolverClientOptions struct {
	// BeaconDir overrides the default beacon directory for auto-join discovery.
	// If empty, beacon.DefaultBeaconDir() is used.
	BeaconDir string

	// ConfigTransportFunc implements §12 config-over-beacon endpoint precedence.
	// When non-nil, autoJoinViaClient calls it before using the beacon-advertised
	// transport. If the function returns a non-nil Transport for the given campfire
	// ID, that transport is used instead of the beacon transport.
	//
	// The typical implementation resolves the campfire ID against behavior.auto_join
	// entries in the roll-up config (behavior.auto_join beacon strings whose
	// decoded campfire ID matches). Callers that do not need config precedence may
	// leave this nil — beacon-only behaviour is preserved.
	ConfigTransportFunc func(campfireID string) protocol.Transport

	// ProbeTimeout overrides the default post-join probe timeout (§11.3.1).
	// If zero, discovery.DefaultProbeTimeout (5s) is used.
	// Operators of high-latency campfires should set this to 30s or more.
	ProbeTimeout time.Duration

	// Tier2VerifierFunc, when non-nil, is called with the protocol.Client to
	// construct the Tier2Verifier used for post-join verification (§11).
	// When nil, the standard clientTier2Verifier is used.
	// Inject a stub here in tests to control probe/unjoin behaviour.
	Tier2VerifierFunc func(*protocol.Client) discovery.Tier2Verifier
}

// NewResolverFromClient creates a Resolver backed by a protocol.Client.
// Resolve and ListChildren use direct-read via client.Read().
// ListAPI reads naming:api messages via client.Read().
// Invoke uses client.Send()+client.Await() for RPC.
//
// Auto-join is enabled: when the resolver walks the name hierarchy and
// encounters a campfire it hasn't joined, it will attempt to join open
// registries automatically. Invite-only registries return ErrInviteOnly.
func NewResolverFromClient(client *protocol.Client, rootID string, opts ...ResolverClientOptions) *Resolver {
	ct := &clientTransport{client: client}
	r := NewResolver(ct, rootID)

	beaconDir := ""
	var configTransportFunc func(string) protocol.Transport
	probeTimeout := discovery.DefaultProbeTimeout
	var tier2VerifierFunc func(*protocol.Client) discovery.Tier2Verifier
	if len(opts) > 0 {
		beaconDir = opts[0].BeaconDir
		configTransportFunc = opts[0].ConfigTransportFunc
		if opts[0].ProbeTimeout > 0 {
			probeTimeout = opts[0].ProbeTimeout
		}
		tier2VerifierFunc = opts[0].Tier2VerifierFunc
	}

	r.AutoJoinFunc = func(campfireID string) error {
		return autoJoinViaClient(client, campfireID, beaconDir, configTransportFunc, probeTimeout, tier2VerifierFunc)
	}
	return r
}

// Resolve reads the campfire directly for name registration messages.
// No futures, no server process — the campfire IS the nameserver.
func (t *clientTransport) Resolve(ctx context.Context, campfireID string, name string) (*ResolveResponse, error) {
	return Resolve(ctx, t.client, campfireID, name)
}

// ListChildren reads all name registrations from the campfire via direct-read
// and filters by prefix. No futures needed.
func (t *clientTransport) ListChildren(ctx context.Context, campfireID string, prefix string) (*ListResponse, error) {
	regs, err := List(ctx, t.client, campfireID)
	if err != nil {
		return nil, fmt.Errorf("listing registrations: %w", err)
	}

	var entries []ListEntry
	for _, reg := range regs {
		if prefix == "" || strings.HasPrefix(reg.Name, prefix) {
			entries = append(entries, ListEntry{
				Name: reg.Name,
			})
		}
	}
	return &ListResponse{Names: entries}, nil
}

// ErrInviteOnly is returned when auto-join encounters an invite-only campfire.
var ErrInviteOnly = fmt.Errorf("campfire is invite-only; cannot auto-join")

// autoJoinViaClient attempts to ensure the client is a member of the given
// campfire. If already a member, this is a no-op. If not a member, it
// discovers the campfire via beacon scan and joins if the join protocol is open.
// Returns ErrInviteOnly for invite-only campfires.
//
// §12 config-over-beacon precedence: when configTransportFunc is non-nil, it is
// consulted before using the beacon-advertised transport. If it returns a non-nil
// Transport for campfireID, that transport is used and the beacon transport is
// discarded. See cf-discovery-spec.md §12 for the rationale.
//
// §11 post-join verification: after a successful Join, the verifier runs
// ProbeAndObserve. On ErrPostJoinVerificationFailed (send-not-ack), unjoin-declaration
// is posted and the client leaves. On ErrPostJoinVerificationLatency (send-ack,
// observe-timeout), the join is accepted with a warning log (§11.3.1 latency path).
func autoJoinViaClient(
	client *protocol.Client,
	campfireID string,
	beaconDirOverride string,
	configTransportFunc func(string) protocol.Transport,
	probeTimeout time.Duration,
	tier2VerifierFunc func(*protocol.Client) discovery.Tier2Verifier,
) error {
	m, err := client.GetMembership(campfireID)
	if err != nil {
		return fmt.Errorf("checking membership: %w", err)
	}
	if m != nil {
		return nil // already a member
	}

	// §12: Check config-declared transport BEFORE beacon scan.
	// Config is trusted at the same level as local identity; beacon is an
	// untrusted hint that the campfire operator controls.
	if configTransportFunc != nil {
		if configTransport := configTransportFunc(campfireID); configTransport != nil {
			log.Printf("[DEBUG] naming: config endpoint overrides beacon for campfire %s", shortID(campfireID))
			_, err = client.Join(protocol.JoinRequest{
				CampfireID: campfireID,
				Transport:  configTransport,
			})
			if err != nil {
				if strings.Contains(err.Error(), "invite-only") {
					return ErrInviteOnly
				}
				return fmt.Errorf("auto-join (config endpoint): %w", err)
			}
			return postJoinVerify(context.Background(), client, campfireID, probeTimeout, tier2VerifierFunc)
		}
	}

	// No config entry: fall back to beacon scan (existing behaviour).
	beaconDir := beaconDirOverride
	if beaconDir == "" {
		beaconDir = beacon.DefaultBeaconDir()
	}
	beacons, err := beacon.Scan(beaconDir)
	if err != nil {
		return fmt.Errorf("scanning beacons: %w", err)
	}

	for _, b := range beacons {
		if b.CampfireIDHex() != campfireID {
			continue
		}

		// Found the beacon. Check join protocol.
		if b.JoinProtocol == "invite-only" {
			return ErrInviteOnly
		}

		// Build join request from beacon transport config.
		transport, err := transportFromBeacon(b)
		if err != nil {
			return fmt.Errorf("building transport from beacon: %w", err)
		}
		_, err = client.Join(protocol.JoinRequest{
			CampfireID: campfireID,
			Transport:  transport,
		})
		if err != nil {
			if strings.Contains(err.Error(), "invite-only") {
				return ErrInviteOnly
			}
			return fmt.Errorf("auto-join: %w", err)
		}
		return postJoinVerify(context.Background(), client, campfireID, probeTimeout, tier2VerifierFunc)
	}

	return fmt.Errorf("no beacon found for campfire %s", shortID(campfireID))
}

// postJoinVerify runs §11 post-join probe-write-then-observe after a successful
// transport-level join. On verification failure (send-not-ack), posts unjoin-declaration
// and leaves. On latency (send-ack, observe-timeout), logs a warning and continues.
func postJoinVerify(
	ctx context.Context,
	client *protocol.Client,
	campfireID string,
	probeTimeout time.Duration,
	tier2VerifierFunc func(*protocol.Client) discovery.Tier2Verifier,
) error {
	var verifier discovery.Tier2Verifier
	if tier2VerifierFunc != nil {
		verifier = tier2VerifierFunc(client)
	} else {
		verifier = discovery.NewClientTier2Verifier(client)
	}

	err := verifier.ProbeAndObserve(ctx, campfireID, probeTimeout)
	switch {
	case errors.Is(err, discovery.ErrPostJoinVerificationFailed):
		// Send-not-acknowledged: suppression confirmed — post declaration and leave.
		// probeMsgID is unknown because Send failed; use empty string.
		_ = verifier.PostUnjoinDeclaration(ctx, campfireID, "")
		_ = client.Leave(campfireID)
		return discovery.ErrPostJoinVerificationFailed
	case errors.Is(err, discovery.ErrPostJoinVerificationLatency):
		// Send-acknowledged but observe-timeout: high-latency path (§11.3.1).
		// Do NOT unjoin — accept the join with a warning.
		log.Printf("[WARN] naming: post-join probe timed out but send was acknowledged for campfire %s — accepting as latency (§11.3.1), not suppression", shortID(campfireID))
		return nil
	case err == nil:
		return nil // verification passed
	default:
		return fmt.Errorf("post-join verification: %w", err)
	}
}

// transportFromBeacon converts a beacon's transport config to a protocol.Transport.
func transportFromBeacon(b beacon.Beacon) (protocol.Transport, error) {
	switch b.Transport.Protocol {
	case "filesystem":
		dir := b.Transport.Config["dir"]
		if dir == "" {
			return nil, fmt.Errorf("filesystem beacon missing dir config")
		}
		return &protocol.FilesystemTransport{Dir: dir}, nil
	default:
		return nil, fmt.Errorf("unsupported beacon transport protocol: %s", b.Transport.Protocol)
	}
}

// ListAPI reads naming:api messages from the given campfire via client.Read().
func (t *clientTransport) ListAPI(ctx context.Context, campfireID string) ([]APIDeclaration, error) {
	result, err := t.client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		Tags:       []string{TagAPI},
	})
	if err != nil {
		return nil, fmt.Errorf("reading api declarations: %w", err)
	}

	var decls []APIDeclaration
	for _, msg := range result.Messages {
		var decl APIDeclaration
		if err := json.Unmarshal(msg.Payload, &decl); err != nil {
			continue // skip malformed declarations
		}
		decls = append(decls, decl)
	}
	return decls, nil
}

// Invoke sends a naming:api-invoke future and awaits fulfillment.
func (t *clientTransport) Invoke(ctx context.Context, campfireID string, req *InvokeRequest) (*InvokeResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	msgID, err := t.sendFuture(ctx, campfireID, payload, []string{TagAPIInvoke})
	if err != nil {
		return nil, fmt.Errorf("sending invoke future: %w", err)
	}

	fulfillment, err := t.awaitFulfillment(ctx, campfireID, msgID)
	if err != nil {
		return nil, fmt.Errorf("awaiting invoke fulfillment: %w", err)
	}

	var resp InvokeResponse
	if err := json.Unmarshal(fulfillment, &resp); err != nil {
		return nil, fmt.Errorf("parsing invoke response: %w", err)
	}
	return &resp, nil
}

// sendFuture sends a message with the given tags plus "future" and returns its ID.
func (t *clientTransport) sendFuture(ctx context.Context, campfireID string, payload []byte, tags []string) (string, error) {
	tags = append(tags, "future")
	msg, err := t.client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    payload,
		Tags:       tags,
	})
	if err != nil {
		return "", fmt.Errorf("sending future: %w", err)
	}
	return msg.ID, nil
}

// awaitFulfillment polls via client.Await() for a message fulfilling targetMsgID.
// Honours ctx cancellation by using a deadline derived from ctx.
func (t *clientTransport) awaitFulfillment(ctx context.Context, campfireID, targetMsgID string) ([]byte, error) {
	var timeout time.Duration
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	} else {
		timeout = DefaultResolutionTimeout
	}

	rec, err := t.client.Await(ctx, protocol.AwaitRequest{
		CampfireID:   campfireID,
		TargetMsgID:  targetMsgID,
		Timeout:      timeout,
		PollInterval: 200 * time.Millisecond,
	})
	if err != nil {
		return nil, err
	}
	return rec.Payload, nil
}

// PublishAPI sends a naming:api tagged message to campfireID with decl as the
// JSON payload. The message is readable by anyone with access to the campfire
// and is used by naming resolvers to discover the campfire's API endpoints.
func PublishAPI(client *protocol.Client, campfireID string, decl APIDeclaration) error {
	payload, err := json.Marshal(&decl)
	if err != nil {
		return fmt.Errorf("marshalling api declaration: %w", err)
	}

	_, err = client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    payload,
		Tags:       []string{TagAPI},
	})
	if err != nil {
		return fmt.Errorf("publishing api declaration: %w", err)
	}
	return nil
}
