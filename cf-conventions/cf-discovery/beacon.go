// Package discovery implements the cf-discovery convention — Tier 1/2/3 discovery,
// snippet schema, multi-level namespace chains, and rate-limit declarations for
// level:0 preview ops.
//
// This package imports pkg/beacon (re-packaged from cf-beacon per design v2 §6 P2)
// and re-exports the key types for convenience of callers who want a single import.
// The pkg/beacon package holds the canonical beacon implementation; cf-discovery
// adds the higher-level discovery layers on top.
//
// Beacon not_after field: OPEN-024. Default lifetime is ~30 days; field is TAINTED
// (not covered by inner_signature — advisory expiry only).
//
// Layer: L3 convention — imports cf-protocol/* (L1) via pkg/beacon.
// Spec citation: design v2 §3 (discovery), §4.3 (cf-discovery = cf-naming + cf-beacon).
package discovery

import (
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/beacon"
)

// Re-export key types from pkg/beacon for callers who import cf-discovery directly.
// These are type aliases — the underlying concrete type remains beacon.Beacon etc.
// Citation: design v2 §4.3 (cf-discovery merges cf-naming + cf-beacon).
type (
	Beacon            = beacon.Beacon
	TransportConfig   = beacon.TransportConfig
	BeaconSignInput   = beacon.BeaconSignInput
	BeaconDeclaration = beacon.BeaconDeclaration
	BeaconDurability  = beacon.BeaconDurability
	DurabilityError   = beacon.DurabilityError
	RegistrationEntry = beacon.RegistrationEntry
)

// DefaultBeaconNotAfterDuration re-exports the default not_after lifetime from pkg/beacon.
// Default is ~30 days per design v2 §6 P2 (OPEN-024).
const DefaultBeaconNotAfterDuration = beacon.DefaultBeaconNotAfterDuration

// Re-export pkg/beacon functions for callers who import cf-discovery directly.
var (
	New                     = beacon.New
	DefaultBeaconDir        = beacon.DefaultBeaconDir
	Publish                 = beacon.Publish
	Remove                  = beacon.Remove
	Scan                    = beacon.Scan
	MarshalInnerSignInput   = beacon.MarshalInnerSignInput
	MarshalInnerSignInputNoPath = beacon.MarshalInnerSignInputNoPath
	SignDeclaration             = beacon.SignDeclaration
	SignDeclarationThreshold    = beacon.SignDeclarationThreshold
	VerifyDeclaration           = beacon.VerifyDeclaration
	IsDeclarationExpired        = beacon.IsDeclarationExpired
	DeclarationToBeacon         = beacon.DeclarationToBeacon
	BeaconToDeclaration         = beacon.BeaconToDeclaration
	ScanCampfire                = beacon.ScanCampfire
	ScanAllMemberships          = beacon.ScanAllMemberships
	CheckBeaconDurability       = beacon.CheckBeaconDurability
)

// Tag constants re-exported from pkg/beacon.
const (
	TagBeacon   = beacon.TagBeacon
	TagWithdraw = beacon.TagWithdraw
)

// NewWithExpiry creates a signed beacon with an explicit not_after advisory expiry.
// Pass a zero time.Time to create a beacon with no expiry (not_after = 0).
// The not_after field is TAINTED — not included in the signing input, advisory only.
// Citation: OPEN-024, design v2 §6 P2.
func NewWithExpiry(
	pub []byte,
	priv []byte,
	joinProtocol string,
	receptionReqs []string,
	transport TransportConfig,
	description string,
	notAfter time.Time,
) (*Beacon, error) {
	b, err := beacon.New(pub, priv, joinProtocol, receptionReqs, transport, description)
	if err != nil {
		return nil, err
	}
	if notAfter.IsZero() {
		b.NotAfter = 0
	} else {
		b.NotAfter = notAfter.Unix()
	}
	// No re-sign needed — NotAfter is TAINTED (not in BeaconSignInput).
	return b, nil
}

// SignDeclarationWithExpiry produces a BeaconDeclaration with an explicit not_after.
// Pass a zero time.Time to omit the not_after field (override the default ~30d).
// The not_after field is TAINTED — not included in the inner_signature signing input.
func SignDeclarationWithExpiry(
	pub []byte,
	priv []byte,
	endpoint string,
	transport string,
	description string,
	joinProtocol string,
	notAfter time.Time,
	path ...[]string,
) (*BeaconDeclaration, error) {
	d, err := beacon.SignDeclaration(pub, priv, endpoint, transport, description, joinProtocol, path...)
	if err != nil {
		return nil, err
	}
	if notAfter.IsZero() {
		d.NotAfter = 0
	} else {
		d.NotAfter = notAfter.Unix()
	}
	return d, nil
}

// ScanFresh reads beacon files from the beacon directory, filtering out expired beacons.
// A beacon is expired if now.Unix() > b.NotAfter (and NotAfter != 0).
// Beacons with NotAfter=0 are always included (no expiry declared).
func ScanFresh(beaconDir string, now time.Time) ([]Beacon, error) {
	all, err := beacon.Scan(beaconDir)
	if err != nil {
		return nil, err
	}
	fresh := all[:0]
	for _, b := range all {
		if !b.IsExpired(now) {
			fresh = append(fresh, b)
		}
	}
	return fresh, nil
}

// ScanRegistrations reads beacon:registration messages from the store and
// validates durability tags as a post-pass. Forwards to pkg/beacon.ScanRegistrations.
func ScanRegistrations(s store.MessageStore, campfireID string, now time.Time) ([]RegistrationEntry, error) {
	return beacon.ScanRegistrations(s, campfireID, now)
}
