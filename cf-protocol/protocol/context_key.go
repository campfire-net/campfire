package protocol

// context_key.go — context key delegation constants (campfireagent-db1: active
// delegation logic removed; locality resolves via L4 cf-discovery).
//
// These constants are retained because the campfireSubdir and related names are
// used elsewhere in the substrate (init.go, etc.).

const (
	campfireSubdir      = ".campfire"
	contextKeyPubFile   = "context-key.pub"
	contextKeyIdentFile = "context-key.json"
	delegationCertFile  = "delegation.cert"
	centerSentinelFile  = "center"
	delegationTagPrefix = "context-key-delegation"
	delegationMsgPrefix = "context-key-delegation:"
	certSignPrefix      = "delegate:"
)

// maybeIssueContextKeyDelegation is a no-op after campfireagent-db1.
// Center-finding logic (walk-up + context key delegation) has been removed from
// the substrate. Locality resolution is L4 work (cf-discovery handles it).
//
// The function signature is retained to avoid churn in init.go callers; it will
// be removed in a subsequent substrate cleanup pass.
func (c *Client) maybeIssueContextKeyDelegation(_ string) error {
	return nil
}
