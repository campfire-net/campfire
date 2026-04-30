// surface_transport.go — transport, transport/fs, transport/http re-exports
// Part of the cf-protocol public surface (campfireagent-401d).
// See surface.go for overview and naming conventions.

package protocol

import (
	internaltransport "github.com/campfire-net/campfire/cf-protocol/internal/transport"
	fs "github.com/campfire-net/campfire/cf-protocol/internal/transport/fs"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/internal/transport/http"
)

// ─── transport package re-exports ────────────────────────────────────────────

// TransportType is the discriminant for the active transport kind.
type TransportType = internaltransport.Type

// Transport type constants.
const (
	TransportTypeFilesystem = internaltransport.TypeFilesystem
	TransportTypePeerHTTP   = internaltransport.TypePeerHTTP
	// TransportTypeGitHub is a tombstone constant for legacy store records.
	// The GitHub transport was removed in v0.30.0. Do not use for new campfires.
	TransportTypeGitHub = internaltransport.TypeGitHub
)

// GitHubTransportPrefix is retained for legacy store record detection only.
// The GitHub transport was removed in v0.30.0.
//
// Deprecated: do not use. This constant exists only for backward compatibility
// with old campfire_memberships rows that have a "github:..." TransportDir.
const GitHubTransportPrefix = internaltransport.GitHubTransportPrefix

// ResolveTransportType resolves the transport type from a Membership record.
var ResolveTransportType = internaltransport.ResolveType

// ─── transport/fs package re-exports ─────────────────────────────────────────

// FSTransport is the filesystem transport implementation.
// Re-exported from cf-protocol/internal/transport/fs.
type FSTransport = fs.Transport

// FSPushSubscriber is the push-subscriber record for filesystem transport.
type FSPushSubscriber = fs.PushSubscriber

// NewFSTransport creates a new filesystem transport with the given base directory.
var NewFSTransport = fs.New

// NewFSTransportPathRooted creates a filesystem transport rooted at the given path.
var NewFSTransportPathRooted = fs.NewPathRooted

// FSTransportForDir returns a filesystem transport rooted at dir (static function).
var FSTransportForDir = fs.ForDir

// FSTransportDefaultBaseDir returns the default base directory for the filesystem transport.
var FSTransportDefaultBaseDir = fs.DefaultBaseDir

// ─── transport/http package re-exports ───────────────────────────────────────

// HTTPTransport is the HTTP campfire transport.
// Re-exported from cf-protocol/internal/transport/http.
type HTTPTransport = cfhttp.Transport

// HTTPTLSConfig holds TLS certificate and key file paths.
type HTTPTLSConfig = cfhttp.TLSConfig

// HTTPPeerInfo holds peer connection info.
type HTTPPeerInfo = cfhttp.PeerInfo

// HTTPJoinResult holds the result of a peer join.
type HTTPJoinResult = cfhttp.JoinResult

// HTTPJoinOptions configures a peer join.
type HTTPJoinOptions = cfhttp.JoinOptions

// HTTPMembershipEvent is a membership change notification.
type HTTPMembershipEvent = cfhttp.MembershipEvent

// HTTPCampfireKeyProvider is a function that returns the campfire key pair.
type HTTPCampfireKeyProvider = cfhttp.CampfireKeyProvider

// HTTPThresholdShareProvider is a function that returns a FROST threshold share.
type HTTPThresholdShareProvider = cfhttp.ThresholdShareProvider

// HTTPRekeyRequest is the rekey request payload.
type HTTPRekeyRequest = cfhttp.RekeyRequest

// HTTPCreateCampfireRequest is the relay create-campfire request.
type HTTPCreateCampfireRequest = cfhttp.CreateCampfireRequest

// HTTPCreateCampfireResponse is the relay create-campfire response.
type HTTPCreateCampfireResponse = cfhttp.CreateCampfireResponse

// HTTPRelayInfoResponse is the relay info response.
type HTTPRelayInfoResponse = cfhttp.RelayInfoResponse

// HTTPCampfireDescriptor describes a campfire for relay registration.
type HTTPCampfireDescriptor = cfhttp.CampfireDescriptor

// HTTPAgentDescriptor describes an agent for relay registration.
type HTTPAgentDescriptor = cfhttp.AgentDescriptor

// HTTPPeerEntry is a peer entry in a join response.
type HTTPPeerEntry = cfhttp.PeerEntry

// HTTPCoSigner is a FROST co-signer.
type HTTPCoSigner = cfhttp.CoSigner

// NewHTTPTransport creates a new HTTP transport.
var NewHTTPTransport = cfhttp.New

// HTTPDeliver delivers a message to a peer endpoint.
var HTTPDeliver = cfhttp.Deliver

// HTTPDeliverToAll delivers a message to all peer endpoints.
var HTTPDeliverToAll = cfhttp.DeliverToAll

// HTTPSync syncs messages from a peer endpoint.
var HTTPSync = cfhttp.Sync

// HTTPJoin joins a peer's campfire.
var HTTPJoin = cfhttp.Join

// HTTPNotifyMembership sends a membership notification.
var HTTPNotifyMembership = cfhttp.NotifyMembership

// HTTPPoll polls a relay endpoint for messages.
var HTTPPoll = cfhttp.Poll

// HTTPRegisterOnRelay registers a campfire on a relay.
var HTTPRegisterOnRelay = cfhttp.RegisterOnRelay

// HTTPSendRekey sends a rekey event.
var HTTPSendRekey = cfhttp.SendRekey

// HTTPSendRekeyPhase1 sends the first phase of a rekey.
var HTTPSendRekeyPhase1 = cfhttp.SendRekeyPhase1

// HTTPRunFROSTSign runs a FROST signing session.
var HTTPRunFROSTSign = cfhttp.RunFROSTSign

// HTTPValidateJoinerEndpoint validates a joiner endpoint for SSRF safety.
var HTTPValidateJoinerEndpoint = cfhttp.ValidateJoinerEndpoint

// HTTPOverrideValidateJoinerEndpointForTest overrides the joiner endpoint validator (test-only).
var HTTPOverrideValidateJoinerEndpointForTest = cfhttp.OverrideValidateJoinerEndpointForTest

// HTTPRestoreValidateJoinerEndpoint restores the joiner endpoint validator (test-only).
var HTTPRestoreValidateJoinerEndpoint = cfhttp.RestoreValidateJoinerEndpoint

// HTTPOverrideHTTPClientForTest overrides the HTTP client (test-only).
var HTTPOverrideHTTPClientForTest = cfhttp.OverrideHTTPClientForTest

// HTTPOverridePollTransportForTest overrides the poll transport (test-only).
var HTTPOverridePollTransportForTest = cfhttp.OverridePollTransportForTest

// HTTPNewSSRFSafeClient creates a new SSRF-safe HTTP client.
var HTTPNewSSRFSafeClient = cfhttp.NewSSRFSafeClient

// HTTPVerifyRequestSignature verifies a relay request signature.
var HTTPVerifyRequestSignature = cfhttp.VerifyRequestSignature

// HTTPDecryptCampfirePrivKey decrypts a campfire private key.
var HTTPDecryptCampfirePrivKey = cfhttp.DecryptCampfirePrivKey

// HTTPDecryptCampfirePrivKeyWithStaticKey decrypts a campfire private key using a static key.
var HTTPDecryptCampfirePrivKeyWithStaticKey = cfhttp.DecryptCampfirePrivKeyWithStaticKey

// HTTPValidateCampfirePrivKey validates a campfire private key.
var HTTPValidateCampfirePrivKey = cfhttp.ValidateCampfirePrivKey

// HTTPHkdfSHA256 derives a key using HKDF-SHA256.
var HTTPHkdfSHA256 = cfhttp.HkdfSHA256

// HTTPAESGCMEncrypt encrypts data with AES-GCM.
var HTTPAESGCMEncrypt = cfhttp.AESGCMEncrypt
