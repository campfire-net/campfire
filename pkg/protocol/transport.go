package protocol

import cfhttp "github.com/campfire-net/campfire/pkg/transport/http"

// Transport is the sealed interface implemented by all transport config types.
// Callers pass a concrete transport config to CreateRequest and JoinRequest.
type Transport interface {
	// TransportType returns the string identifier for the transport
	// (e.g. "filesystem", "p2p-http", "github").
	TransportType() string
}

// FilesystemTransport configures the local filesystem transport.
// Dir is the base directory used by the transport (the root under which
// campfire-specific subdirectories are created).
type FilesystemTransport struct {
	Dir string
}

// TransportType returns "filesystem".
func (FilesystemTransport) TransportType() string { return "filesystem" }

// P2PHTTPTransport configures the P2P HTTP transport.
// Transport is the running cfhttp.Transport instance (must be started before use).
// MyEndpoint is this node's publicly reachable HTTP endpoint, e.g. "http://host:port".
// PeerEndpoint is the HTTP endpoint of an existing campfire member to join through.
// Required when calling Join. Example: "http://127.0.0.1:9001".
// Dir is the directory to store P2P HTTP campfire state CBOR files.
// Optional: if empty, a temp directory is used.
type P2PHTTPTransport struct {
	Transport    *cfhttp.Transport
	MyEndpoint   string
	PeerEndpoint string
	Dir          string
}

// TransportType returns "p2p-http".
func (P2PHTTPTransport) TransportType() string { return "p2p-http" }

// GitHubTransport configures the GitHub-backed transport.
// Owner, Repo, Branch, and Dir identify the target repository and path.
// Token is an optional personal access token for private repos or authenticated writes.
type GitHubTransport struct {
	Owner  string
	Repo   string
	Branch string
	Dir    string
	Token  string
}

// TransportType returns "github".
func (GitHubTransport) TransportType() string { return "github" }
