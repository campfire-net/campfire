package store

// Compile-time interface satisfaction checks.
// These blank-identifier assignments confirm that *Store implements each interface.
// A build failure here means a method was removed or renamed.
var (
	_ MembershipStore = (*Store)(nil)
	_ MessageStore    = (*Store)(nil)
	_ PeerStore       = (*Store)(nil)
	_ ThresholdStore  = (*Store)(nil)
)
