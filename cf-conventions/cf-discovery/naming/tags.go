// Tag constants for the campfire naming protocol.
//
// Tags in the "naming:" namespace are produced and consumed by the naming
// package. Convention declarations may not produce them (the "naming:" prefix
// is in the denied list in pkg/convention/parser.go).
package naming

const (
	// TagPrefix is the reserved namespace prefix for all naming protocol messages.
	TagPrefix = "naming:"

	// TagAPI is the tag for API-declaration messages that advertise a naming
	// server's available endpoints.
	TagAPI = "naming:api"

	// TagAPIInvoke is the tag for API-invoke request messages.
	TagAPIInvoke = "naming:api-invoke"

	// TagResolve is the tag for naming resolution request messages.
	TagResolve = "naming:resolve"

	// TagResolveList is the tag for naming list-children request messages.
	TagResolveList = "naming:resolve-list"

	// TagUnregister is the tag for name-unregistration messages.
	TagUnregister = "naming:unregister"

	// TagNamePrefix is the prefix for per-name registration tags.
	// The full tag for a specific name is TagNamePrefix + name.
	TagNamePrefix = "naming:name:"
)
