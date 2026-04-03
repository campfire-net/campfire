// Tag constants for the campfire routing protocol.
//
// Tags in the "routing:" namespace are used by the HTTP transport layer to
// carry beacon and withdraw payloads between campfire nodes.
package beacon

const (
	// TagBeacon is the tag for routing:beacon messages. A beacon message carries
	// a BeaconDeclaration payload advertising a campfire's reachable endpoint.
	TagBeacon = "routing:beacon"

	// TagWithdraw is the tag for routing:withdraw messages. A withdraw message
	// removes a previously-advertised beacon from the routing table.
	TagWithdraw = "routing:withdraw"
)
