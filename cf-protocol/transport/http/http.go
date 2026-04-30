// Package http — public forwarding wrapper (campfireagent-401d).
//
// This package re-exports the HTTP transport from cf-protocol/internal/transport/http.
// It exists so that external consumers (cmd/, bridge/, pkg/, tests/) can keep their
// existing imports while the substrate lives in internal/.
//
// New code should import cf-protocol/protocol instead of this package directly.
package http

import cfhttp "github.com/campfire-net/campfire/cf-protocol/internal/transport/http"

// Re-export types.
type (
	Transport                   = cfhttp.Transport
	TLSConfig                   = cfhttp.TLSConfig
	PeerInfo                    = cfhttp.PeerInfo
	JoinResult                  = cfhttp.JoinResult
	JoinOptions                 = cfhttp.JoinOptions
	MembershipEvent             = cfhttp.MembershipEvent
	CampfireKeyProvider         = cfhttp.CampfireKeyProvider
	CampfireDeliveryModesProvider = cfhttp.CampfireDeliveryModesProvider
	ThresholdShareProvider      = cfhttp.ThresholdShareProvider
	RekeyRequest                = cfhttp.RekeyRequest
	CreateCampfireRequest       = cfhttp.CreateCampfireRequest
	CreateCampfireResponse      = cfhttp.CreateCampfireResponse
	RelayInfoResponse           = cfhttp.RelayInfoResponse
	CampfireDescriptor          = cfhttp.CampfireDescriptor
	AgentDescriptor             = cfhttp.AgentDescriptor
	PeerEntry                   = cfhttp.PeerEntry
	CoSigner                    = cfhttp.CoSigner
	BloomFilter                 = cfhttp.BloomFilter
	RouteEntry                  = cfhttp.RouteEntry
	RoutingTable                = cfhttp.RoutingTable
	PollBroker                  = cfhttp.PollBroker
	DedupTable                  = cfhttp.DedupTable
)

// Re-export functions.
var (
	New                                  = cfhttp.New
	Deliver                              = cfhttp.Deliver
	DeliverToAll                         = cfhttp.DeliverToAll
	Sync                                 = cfhttp.Sync
	Join                                 = cfhttp.Join
	AdmitOnRelay                         = cfhttp.AdmitOnRelay
	NotifyMembership                     = cfhttp.NotifyMembership
	Poll                                 = cfhttp.Poll
	FetchRelayInfo                       = cfhttp.FetchRelayInfo
	RegisterOnRelay                      = cfhttp.RegisterOnRelay
	SendRekey                            = cfhttp.SendRekey
	SendRekeyPhase1                      = cfhttp.SendRekeyPhase1
	SendSignRound                        = cfhttp.SendSignRound
	RunFROSTSign                         = cfhttp.RunFROSTSign
	ValidateJoinerEndpoint               = cfhttp.ValidateJoinerEndpoint
	OverrideValidateJoinerEndpointForTest = cfhttp.OverrideValidateJoinerEndpointForTest
	RestoreValidateJoinerEndpoint        = cfhttp.RestoreValidateJoinerEndpoint
	OverrideHTTPClientForTest            = cfhttp.OverrideHTTPClientForTest
	OverridePollTransportForTest         = cfhttp.OverridePollTransportForTest
	NewSSRFSafeClient                    = cfhttp.NewSSRFSafeClient
	VerifyRequestSignature               = cfhttp.VerifyRequestSignature
	DecryptCampfirePrivKey               = cfhttp.DecryptCampfirePrivKey
	DecryptCampfirePrivKeyWithStaticKey  = cfhttp.DecryptCampfirePrivKeyWithStaticKey
	ValidateCampfirePrivKey              = cfhttp.ValidateCampfirePrivKey
	HkdfSHA256                           = cfhttp.HkdfSHA256
	AESGCMEncrypt                        = cfhttp.AESGCMEncrypt
)
