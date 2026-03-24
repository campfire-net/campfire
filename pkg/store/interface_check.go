package store

// Compile-time interface satisfaction checks.
// These blank-identifier assignments confirm that *SQLiteStore implements each interface.
// A build failure here means a method was removed or renamed.
var (
	_ MembershipStore = (*SQLiteStore)(nil)
	_ MessageStore    = (*SQLiteStore)(nil)
	_ PeerStore       = (*SQLiteStore)(nil)
	_ ThresholdStore  = (*SQLiteStore)(nil)
	_ Store           = (*SQLiteStore)(nil)
)
