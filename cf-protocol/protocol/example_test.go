package protocol_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// Example_init demonstrates how to initialize a campfire client.
// Init creates an Ed25519 identity (if none exists) and opens a SQLite store,
// both rooted at the given configDir.
func Example_init() {
	dir, err := os.MkdirTemp("", "campfire-example-init-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	client, result, err := protocol.Init(dir)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	fmt.Println("identity created:", result.IdentityCreated)
	fmt.Println("identity path set:", result.IdentityPath != "")
	// Output:
	// identity created: true
	// identity path set: true
}

// Example_initWithConfig demonstrates the recommended InitWithConfig entry
// point that discovers and merges config from the cascade before delegating
// to Init.
func Example_initWithConfig() {
	dir, err := os.MkdirTemp("", "campfire-example-config-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	client, result, err := protocol.InitWithConfig(protocol.WithConfigDir(dir))
	if err != nil {
		panic(err)
	}
	defer client.Close()

	fmt.Println("client ready:", client != nil)
	fmt.Println("identity source:", result.IdentitySource)
	// Output:
	// client ready: true
	// identity source: env
}

// Example_withConfigDir demonstrates passing an explicit configDir to Init.
func Example_withConfigDir() {
	dir, err := os.MkdirTemp("", "campfire-example-cfgdir-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	opt := protocol.WithConfigDir(dir)
	client, result, err := protocol.InitWithConfig(opt)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	fmt.Println("config applied:", filepath.Base(result.IdentityPath) == "identity.json")
	// Output:
	// config applied: true
}

// Example_withScope demonstrates installing a campfire/operation allowlist on
// a client via WithScope. Attempts to access campfires outside the allowlist
// will be refused with ErrScopeDenied.
func Example_withScope() {
	dir, err := os.MkdirTemp("", "campfire-example-scope-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	cfg := protocol.ScopeConfig{
		Campfires:        []string{"aabbcc"},
		OperationClasses: []string{"read"},
	}
	client, _, err := protocol.InitWithConfig(
		protocol.WithConfigDir(dir),
		protocol.WithScope(cfg),
	)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	fmt.Println("client ready:", client != nil)
	// Output:
	// client ready: true
}

// Example_clientClose demonstrates closing a client and releasing its resources.
func Example_clientClose() {
	dir, err := os.MkdirTemp("", "campfire-example-close-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	client, _, err := protocol.Init(dir)
	if err != nil {
		panic(err)
	}

	err = client.Close()
	fmt.Println("closed:", err == nil)
	// Output:
	// closed: true
}

// Example_clientPublicKeyHex demonstrates retrieving the client's hex-encoded
// Ed25519 public key.
func Example_clientPublicKeyHex() {
	dir, err := os.MkdirTemp("", "campfire-example-pubkey-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	client, _, err := protocol.Init(dir)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	pubkey := client.PublicKeyHex()
	fmt.Println("pubkey length:", len(pubkey))
	// Output:
	// pubkey length: 64
}

// Example_createAndJoinFilesystem demonstrates creating a campfire with the
// filesystem transport, then joining it from a second client.
func Example_createAndJoinFilesystem() {
	dir, err := os.MkdirTemp("", "campfire-example-create-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	cfDir := filepath.Join(dir, "campfire-transport")
	if err := os.MkdirAll(cfDir, 0700); err != nil {
		panic(err)
	}

	// Creator client.
	creatorDir := filepath.Join(dir, "creator")
	client, _, err := protocol.Init(creatorDir)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	result, err := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println("campfire id length:", len(result.CampfireID))
	fmt.Println("beacon id set:", result.BeaconID != "")
	// Output:
	// campfire id length: 64
	// beacon id set: true
}

// Example_sendRequest demonstrates constructing a SendRequest value.
// In practice, pass it to Client.Send.
func Example_sendRequest() {
	req := protocol.SendRequest{
		CampfireID: "aabbcc",
		Payload:    []byte(`{"msg":"hello"}`),
		Tags:       []string{"status"},
		Instance:   "my-agent",
	}
	fmt.Println("campfire:", req.CampfireID)
	fmt.Println("tags:", req.Tags)
	// Output:
	// campfire: aabbcc
	// tags: [status]
}

// Example_messageIsBridged demonstrates the IsBridged helper on protocol.Message.
func Example_messageIsBridged() {
	msg := protocol.Message{
		ID:      "abc123",
		Payload: []byte("hello"),
	}
	fmt.Println("bridged:", msg.IsBridged())
	// Output:
	// bridged: false
}

// Example_messageSenderIdentity demonstrates SenderIdentity, which returns
// the SenderCampfireID when available, falling back to Sender.
func Example_messageSenderIdentity() {
	msg := protocol.Message{
		Sender:           "aabbccddeeff",
		SenderCampfireID: "112233445566",
	}
	fmt.Println("identity:", msg.SenderIdentity())
	// Output:
	// identity: 112233445566
}

// Example_readRequest demonstrates constructing a ReadRequest value.
func Example_readRequest() {
	req := protocol.ReadRequest{
		CampfireID:       "aabbcc",
		Tags:             []string{"status"},
		IncludeCompacted: false,
	}
	fmt.Println("campfire:", req.CampfireID)
	fmt.Println("tags:", req.Tags)
	// Output:
	// campfire: aabbcc
	// tags: [status]
}

// Example_subscribeRequest demonstrates constructing a SubscribeRequest value.
func Example_subscribeRequest() {
	req := protocol.SubscribeRequest{
		CampfireID: "aabbcc",
		Tags:       []string{"status"},
	}
	fmt.Println("campfire:", req.CampfireID)
	fmt.Println("tags:", req.Tags)
	// Output:
	// campfire: aabbcc
	// tags: [status]
}

// Example_subscription demonstrates creating and using a Subscription from
// client.Subscribe, then cancelling via context.
func Example_subscription() {
	dir, err := os.MkdirTemp("", "campfire-example-sub-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	client, _, err := protocol.Init(dir)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately — we just want the API shape

	// Subscribe to a non-existent campfire; it errors but the API shape is correct.
	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID: "aabbcc",
	})
	// Drain the channel (it's closed on context cancellation or error).
	for range sub.Messages() {
	}
	fmt.Println("subscription done:", true)
	// Output:
	// subscription done: true
}

// Example_joinRequest demonstrates constructing a JoinRequest for the
// filesystem transport.
func Example_joinRequest() {
	req := protocol.JoinRequest{
		CampfireID: "aabbcc",
		Transport:  protocol.FilesystemTransport{Dir: "/tmp/mycamp"},
	}
	fmt.Println("campfire:", req.CampfireID)
	fmt.Println("transport type:", req.Transport.TransportType())
	// Output:
	// campfire: aabbcc
	// transport type: filesystem
}

// Example_admitRequest demonstrates constructing an AdmitRequest.
func Example_admitRequest() {
	req := protocol.AdmitRequest{
		CampfireID:      "aabbcc",
		MemberPubKeyHex: "ddeeff",
		Role:            "full",
	}
	fmt.Println("campfire:", req.CampfireID)
	fmt.Println("role:", req.Role)
	// Output:
	// campfire: aabbcc
	// role: full
}

// Example_evictRequest demonstrates constructing an EvictRequest.
func Example_evictRequest() {
	req := protocol.EvictRequest{
		CampfireID:      "aabbcc",
		MemberPubKeyHex: "ddeeff",
	}
	fmt.Println("campfire:", req.CampfireID)
	fmt.Println("member:", req.MemberPubKeyHex)
	// Output:
	// campfire: aabbcc
	// member: ddeeff
}

// Example_createRequest demonstrates constructing a CreateRequest.
func Example_createRequest() {
	req := protocol.CreateRequest{
		Transport:  protocol.FilesystemTransport{Dir: "/tmp/mycamp"},
		JoinProtocol: "open",
	}
	fmt.Println("transport type:", req.Transport.TransportType())
	fmt.Println("join protocol:", req.JoinProtocol)
	// Output:
	// transport type: filesystem
	// join protocol: open
}

// Example_filesystemTransport demonstrates creating a FilesystemTransport value.
func Example_filesystemTransport() {
	t := protocol.FilesystemTransport{Dir: "/tmp/campfires"}
	fmt.Println("type:", t.TransportType())
	fmt.Println("dir:", t.Dir)
	// Output:
	// type: filesystem
	// dir: /tmp/campfires
}

// Example_p2pHTTPTransport demonstrates creating a P2PHTTPTransport value.
func Example_p2pHTTPTransport() {
	t := protocol.P2PHTTPTransport{
		MyEndpoint:   "http://localhost:9001",
		PeerEndpoint: "http://peer:9001",
	}
	fmt.Println("type:", t.TransportType())
	fmt.Println("my endpoint:", t.MyEndpoint)
	// Output:
	// type: p2p-http
	// my endpoint: http://localhost:9001
}

// Example_scopeEnforcer demonstrates creating a ScopeEnforcer and checking
// campfire and operation access.
func Example_scopeEnforcer() {
	enforcer := protocol.NewScopeEnforcer(protocol.ScopeConfig{
		Campfires:        []string{"aabbcc"},
		OperationClasses: []string{"read", "write"},
	})

	fmt.Println("allowed campfire:", enforcer.CheckCampfire("aabbcc") == nil)
	fmt.Println("denied campfire:", enforcer.CheckCampfire("zzzzzz") != nil)
	fmt.Println("allowed op:", enforcer.CheckOperation("read") == nil)
	fmt.Println("denied op:", enforcer.CheckOperation("admin") != nil)
	// Output:
	// allowed campfire: true
	// denied campfire: true
	// allowed op: true
	// denied op: true
}

// Example_bridgeOptions demonstrates constructing a BridgeOptions value for
// bidirectional pass-through bridging.
func Example_bridgeOptions() {
	opts := protocol.BridgeOptions{
		Bidirectional: true,
		Forward:       true,
		TagFilter:     []string{"status"},
	}
	fmt.Println("bidirectional:", opts.Bidirectional)
	fmt.Println("forward:", opts.Forward)
	fmt.Println("tag filter:", opts.TagFilter)
	// Output:
	// bidirectional: true
	// forward: true
	// tag filter: [status]
}

// Example_loadConfig demonstrates loading the config cascade from given
// global and project directories.
func Example_loadConfig() {
	globalDir, err := os.MkdirTemp("", "campfire-example-config-global-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(globalDir)

	projectDir, err := os.MkdirTemp("", "campfire-example-config-project-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(projectDir)

	cfg, layers, warnings, err := protocol.LoadConfig(globalDir, projectDir)
	if err != nil {
		panic(err)
	}

	fmt.Println("config loaded:", cfg != nil)
	fmt.Println("warnings:", len(warnings))
	_ = layers
	// Output:
	// config loaded: true
	// warnings: 0
}

// Example_defaultConfig demonstrates obtaining the compiled-in default config.
func Example_defaultConfig() {
	cfg := protocol.DefaultConfig()
	fmt.Println("default backend:", cfg.Identity.Backend)
	// Output:
	// default backend: file
}

// Example_configLayer demonstrates the ConfigLayer struct fields.
func Example_configLayer() {
	layer := protocol.ConfigLayer{
		Path:    "/home/user/.cf/config.toml",
		Source:  "global",
		Skipped: false,
	}
	fmt.Println("source:", layer.Source)
	fmt.Println("skipped:", layer.Skipped)
	// Output:
	// source: global
	// skipped: false
}

// Example_initResult demonstrates the InitResult struct fields.
func Example_initResult() {
	result := protocol.InitResult{
		IdentityCreated: true,
		IdentityPath:    "/home/user/.cf/identity.json",
		StorePath:       "/home/user/.cf/store.db",
		IdentitySource:  "default",
	}
	fmt.Println("created:", result.IdentityCreated)
	fmt.Println("source:", result.IdentitySource)
	// Output:
	// created: true
	// source: default
}

// Example_scopeConfig demonstrates constructing a ScopeConfig value.
func Example_scopeConfig() {
	cfg := protocol.ScopeConfig{
		Campfires:        []string{"aabbcc", "ddeeff"},
		OperationClasses: []string{"read"},
	}
	fmt.Println("campfires:", len(cfg.Campfires))
	fmt.Println("ops:", cfg.OperationClasses)
	// Output:
	// campfires: 2
	// ops: [read]
}

// Example_errNotMember demonstrates that attempting to read a campfire the
// client has not joined returns a typed error.
func Example_errNotMember() {
	dir, err := os.MkdirTemp("", "campfire-example-notmember-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	client, _, err := protocol.Init(dir)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	// Use a valid-format 64-hex campfire ID that the client has not joined.
	notJoinedID := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	_, readErr := client.Read(protocol.ReadRequest{CampfireID: notJoinedID})
	isNotMember := protocol.IsNotMemberError(readErr, nil)
	fmt.Println("err is ErrNotMember:", isNotMember)
	// Output:
	// err is ErrNotMember: true
}

// Example_errScopeDenied demonstrates that ErrScopeDenied is returned when
// a scope check fails.
func Example_errScopeDenied() {
	dir, err := os.MkdirTemp("", "campfire-example-scopedeny-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	// Client restricted to a single campfire ID (64-hex format).
	allowedID := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	client, _, err := protocol.InitWithConfig(
		protocol.WithConfigDir(dir),
		protocol.WithScope(protocol.ScopeConfig{
			Campfires:        []string{allowedID},
			OperationClasses: []string{"read"},
		}),
	)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	otherID := "1122334455667788990011223344556677889900112233445566778899001122"
	_, readErr := client.Read(protocol.ReadRequest{CampfireID: otherID})
	fmt.Println("scope denied:", readErr != nil)
	// Output:
	// scope denied: true
}

// Example_messageFromRecord demonstrates converting a low-level MessageRecord
// to the higher-level protocol.Message type.
func Example_messageFromRecord() {
	rec := protocol.MessageRecord{
		ID:         "abc123",
		CampfireID: "cfid",
		Sender:     "pubkey",
		Payload:    []byte("hello"),
	}
	msg := protocol.MessageFromRecord(rec)
	fmt.Println("id:", msg.ID)
	fmt.Println("sender:", msg.Sender)
	// Output:
	// id: abc123
	// sender: pubkey
}
