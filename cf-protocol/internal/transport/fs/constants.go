package fs

// NanosWidth is the width of the nanosecond timestamp field in message filenames.
// Filenames are formatted as "<19-nanos>-<message-id>.cbor" where nanos is zero-padded
// to 19 digits. This width accommodates Unix nanoseconds up to year 2262.
const NanosWidth = 19

// CampfireIDHexLen is the hex-encoded length of an Ed25519 public key.
// Ed25519 public keys are 32 bytes; encoded in hex (2 chars per byte), they are 64 chars.
// Used for hex-formatting pubkeys in member IDs and campfire directory names.
const CampfireIDHexLen = 64
