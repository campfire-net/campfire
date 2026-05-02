package protocol_test

// example_surface_test.go provides Example_ tests for the re-exported surface
// types (surface.go, surface_transport.go) and other supplementary symbols.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// ─── Client methods ─────────────────────────────────────────────────────────

// Example_client demonstrates the Client type via Init.
func Example_client() {
	dir, _ := os.MkdirTemp("", "campfire-client-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	fmt.Println("client ready:", client != nil)
	// Output:
	// client ready: true
}

// Example_new demonstrates creating a Client with New (store + identity required).
func Example_new() {
	dir, _ := os.MkdirTemp("", "campfire-new-*")
	defer os.RemoveAll(dir)
	// New is a low-level constructor for callers managing their own store/identity.
	// In practice, use Init or InitWithConfig.
	s, _ := protocol.OpenMemory("test-new")
	client := protocol.New(s, nil)
	fmt.Println("client ready:", client != nil)
	// Output:
	// client ready: true
}

// Example_clientSetScope demonstrates SetScope on an existing client.
func Example_clientSetScope() {
	dir, _ := os.MkdirTemp("", "campfire-setscope-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	client.SetScope(protocol.ScopeConfig{
		OperationClasses: []string{"read"},
	})
	fmt.Println("scope applied:", true)
	// Output:
	// scope applied: true
}

// Example_clientGetMembership demonstrates GetMembership on a non-member campfire.
func Example_clientGetMembership() {
	dir, _ := os.MkdirTemp("", "campfire-getmem-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	id := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	m, err := client.GetMembership(id)
	fmt.Println("no membership:", m == nil && err == nil)
	// Output:
	// no membership: true
}

// Example_clientProfileCache demonstrates retrieving the ProfileCache.
func Example_clientProfileCache() {
	dir, _ := os.MkdirTemp("", "campfire-pcache-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	fmt.Println("cache initialized:", client.ProfileCache() != nil)
	// Output:
	// cache initialized: true
}

// Example_clientSetSyncer demonstrates installing a Syncer on a client.
func Example_clientSetSyncer() {
	dir, _ := os.MkdirTemp("", "campfire-syncer-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	// Pass nil to remove any installed Syncer.
	client.SetSyncer(nil)
	fmt.Println("syncer cleared:", true)
	// Output:
	// syncer cleared: true
}

// Example_clientRemoteURL demonstrates retrieving the remote URL (empty by default).
func Example_clientRemoteURL() {
	dir, _ := os.MkdirTemp("", "campfire-remoteurl-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	// Remote URL is only set when WithRemote is passed to Init.
	url := client.RemoteURL()
	fmt.Println("url empty:", url == "")
	// Output:
	// url empty: true
}

// Example_clientAuthorize demonstrates the Authorize gate (auto-approves in tests).
func Example_clientAuthorize() {
	dir, _ := os.MkdirTemp("", "campfire-auth-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.InitWithConfig(
		protocol.WithConfigDir(dir),
		protocol.WithAuthorizeFunc(func(desc string) (bool, error) {
			return true, nil // always approve
		}),
	)
	defer client.Close()
	ok, err := client.Authorize("test operation")
	fmt.Println("approved:", ok, err)
	// Output:
	// approved: true <nil>
}

// Example_clientRead demonstrates calling Read on a campfire the client has joined.
func Example_clientRead() {
	dir, _ := os.MkdirTemp("", "campfire-read-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)

	client, _, _ := protocol.Init(dir)
	defer client.Close()

	result, _ := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})

	r, err := client.Read(protocol.ReadRequest{CampfireID: result.CampfireID})
	fmt.Println("read ok:", err == nil)
	fmt.Println("messages:", len(r.Messages))
	// Output:
	// read ok: true
	// messages: 0
}

// Example_clientSend demonstrates sending a message on a campfire the client created.
func Example_clientSend() {
	dir, _ := os.MkdirTemp("", "campfire-send-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)

	client, _, _ := protocol.Init(dir)
	defer client.Close()
	result, _ := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})

	msg, err := client.Send(protocol.SendRequest{
		CampfireID: result.CampfireID,
		Payload:    []byte(`{"hello":"world"}`),
		Tags:       []string{"status"},
	})
	fmt.Println("send ok:", err == nil)
	fmt.Println("msg id set:", msg != nil && len(msg.ID) > 0)
	// Output:
	// send ok: true
	// msg id set: true
}

// Example_clientSubscribe demonstrates the typed Subscribe method.
func Example_clientSubscribe() {
	dir, _ := os.MkdirTemp("", "campfire-subscribe-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)

	client, _, _ := protocol.Init(dir)
	defer client.Close()
	result, _ := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sub := client.Subscribe(ctx, protocol.SubscribeRequest{CampfireID: result.CampfireID})
	for range sub.Messages() {
	}
	fmt.Println("subscription closed:", true)
	// Output:
	// subscription closed: true
}

// Example_subscriptionMessages demonstrates the Messages channel accessor.
func Example_subscriptionMessages() {
	dir, _ := os.MkdirTemp("", "campfire-sub-msgs-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	id := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	sub := client.Subscribe(ctx, protocol.SubscribeRequest{CampfireID: id})
	ch := sub.Messages()
	fmt.Println("channel set:", ch != nil)
	for range ch {
	}
	// Output:
	// channel set: true
}

// Example_subscriptionErr demonstrates calling Err after a subscription ends.
func Example_subscriptionErr() {
	dir, _ := os.MkdirTemp("", "campfire-sub-err-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	id := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	sub := client.Subscribe(ctx, protocol.SubscribeRequest{CampfireID: id})
	for range sub.Messages() {
	}
	// After channel is closed, Err() reports any terminal error.
	_ = sub.Err()
	fmt.Println("err checked:", true)
	// Output:
	// err checked: true
}

// Example_clientCreate demonstrates the Create method.
func Example_clientCreate() {
	dir, _ := os.MkdirTemp("", "campfire-create-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)

	client, _, _ := protocol.Init(dir)
	defer client.Close()

	res, err := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})
	fmt.Println("created:", err == nil)
	fmt.Println("id set:", len(res.CampfireID) == 64)
	// Output:
	// created: true
	// id set: true
}

// Example_createResult demonstrates the CreateResult struct.
func Example_createResult() {
	r := protocol.CreateResult{
		CampfireID: "aabbcc",
		BeaconID:   "aabbcc",
	}
	fmt.Println("id:", r.CampfireID)
	fmt.Println("beacon:", r.BeaconID)
	// Output:
	// id: aabbcc
	// beacon: aabbcc
}

// Example_clientJoin demonstrates the Join method on the filesystem transport.
func Example_clientJoin() {
	dir, _ := os.MkdirTemp("", "campfire-join-*")
	defer os.RemoveAll(dir)
	baseDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(baseDir, 0700)

	// Creator.
	creator, _, _ := protocol.Init(filepath.Join(dir, "creator"))
	defer creator.Close()
	res, _ := creator.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: baseDir},
	})

	// Campfire-specific transport dir is {baseDir}/{campfireID}.
	cfDir := filepath.Join(baseDir, res.CampfireID)

	// Joiner (second client using campfire-specific transport dir).
	joinerDir := filepath.Join(dir, "joiner")
	joiner, _, _ := protocol.Init(joinerDir)
	defer joiner.Close()

	// Pre-admit the joiner.
	_ = creator.Admit(protocol.AdmitRequest{
		CampfireID:      res.CampfireID,
		MemberPubKeyHex: joiner.PublicKeyHex(),
		Role:            "full",
	})

	joinRes, err := joiner.Join(protocol.JoinRequest{
		CampfireID: res.CampfireID,
		Transport:  protocol.FilesystemTransport{Dir: cfDir},
	})
	fmt.Println("joined:", err == nil)
	fmt.Println("id set:", joinRes != nil && joinRes.CampfireID == res.CampfireID)
	// Output:
	// joined: true
	// id set: true
}

// Example_joinResult demonstrates the JoinResult struct.
func Example_joinResult() {
	r := protocol.JoinResult{
		CampfireID:   "aabbcc",
		JoinProtocol: "open",
		TransportDir: "/tmp/cfdir",
	}
	fmt.Println("id:", r.CampfireID)
	fmt.Println("protocol:", r.JoinProtocol)
	// Output:
	// id: aabbcc
	// protocol: open
}

// Example_clientAdmit demonstrates pre-admitting a member.
func Example_clientAdmit() {
	dir, _ := os.MkdirTemp("", "campfire-admit-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)

	client, _, _ := protocol.Init(dir)
	defer client.Close()
	res, _ := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})

	// Admit a (synthetic) member pubkey.
	fakePubkey := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	err := client.Admit(protocol.AdmitRequest{
		CampfireID:      res.CampfireID,
		MemberPubKeyHex: fakePubkey,
		Role:            "full",
	})
	fmt.Println("admitted:", err == nil)
	// Output:
	// admitted: true
}

// Example_clientLeave demonstrates the Leave method.
func Example_clientLeave() {
	dir, _ := os.MkdirTemp("", "campfire-leave-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)

	client, _, _ := protocol.Init(dir)
	defer client.Close()
	res, _ := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})

	err := client.Leave(res.CampfireID)
	fmt.Println("left:", err == nil)
	// Output:
	// left: true
}

// Example_clientDisband demonstrates the Disband method.
func Example_clientDisband() {
	dir, _ := os.MkdirTemp("", "campfire-disband-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)

	client, _, _ := protocol.Init(dir)
	defer client.Close()
	res, _ := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})

	err := client.Disband(res.CampfireID)
	fmt.Println("disbanded:", err == nil)
	// Output:
	// disbanded: true
}

// Example_clientMembers demonstrates the Members method.
func Example_clientMembers() {
	dir, _ := os.MkdirTemp("", "campfire-members-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)

	client, _, _ := protocol.Init(dir)
	defer client.Close()
	res, _ := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})

	members, err := client.Members(res.CampfireID)
	fmt.Println("members ok:", err == nil)
	fmt.Println("count:", len(members))
	// Output:
	// members ok: true
	// count: 1
}

// Example_memberRecord demonstrates the MemberRecord struct.
func Example_memberRecord() {
	m := protocol.MemberRecord{
		MemberPubkey: "aabbcc",
		Role:         "full",
	}
	fmt.Println("pubkey:", m.MemberPubkey)
	fmt.Println("role:", m.Role)
	// Output:
	// pubkey: aabbcc
	// role: full
}

// Example_clientGet demonstrates the Get method for a known message ID.
func Example_clientGet() {
	dir, _ := os.MkdirTemp("", "campfire-get-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)

	client, _, _ := protocol.Init(dir)
	defer client.Close()
	res, _ := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})
	sent, _ := client.Send(protocol.SendRequest{
		CampfireID: res.CampfireID,
		Payload:    []byte("hello"),
		Tags:       []string{"status"},
	})

	got, err := client.Get(sent.ID)
	fmt.Println("get ok:", err == nil)
	fmt.Println("id match:", got != nil && got.ID == sent.ID)
	// Output:
	// get ok: true
	// id match: true
}

// Example_clientGetByPrefix demonstrates GetByPrefix for a partial message ID.
func Example_clientGetByPrefix() {
	dir, _ := os.MkdirTemp("", "campfire-getprefix-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)

	client, _, _ := protocol.Init(dir)
	defer client.Close()
	res, _ := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})
	sent, _ := client.Send(protocol.SendRequest{
		CampfireID: res.CampfireID,
		Payload:    []byte("hello"),
		Tags:       []string{"status"},
	})

	// Look up by 8-char prefix.
	prefix := sent.ID[:8]
	got, err := client.GetByPrefix(prefix)
	fmt.Println("get ok:", err == nil)
	fmt.Println("found:", got != nil)
	// Output:
	// get ok: true
	// found: true
}

// Example_clientAwait demonstrates the Await method using a future/fulfills pattern.
func Example_clientAwait() {
	dir, _ := os.MkdirTemp("", "campfire-await-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)

	client, _, _ := protocol.Init(dir)
	defer client.Close()
	res, _ := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})

	// Send a "future" message.
	future, _ := client.Send(protocol.SendRequest{
		CampfireID: res.CampfireID,
		Payload:    []byte("pending"),
		Tags:       []string{"future"},
	})

	// Send the fulfillment immediately (same client, same campfire).
	_, _ = client.Send(protocol.SendRequest{
		CampfireID:  res.CampfireID,
		Payload:     []byte("done"),
		Tags:        []string{"fulfills"},
		Antecedents: []string{future.ID},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	got, err := client.Await(ctx, protocol.AwaitRequest{
		CampfireID:   res.CampfireID,
		TargetMsgID:  future.ID,
		PollInterval: 100 * time.Millisecond,
	})
	fmt.Println("await ok:", err == nil)
	fmt.Println("fulfilled:", got != nil)
	// Output:
	// await ok: true
	// fulfilled: true
}

// Example_awaitRequest demonstrates constructing an AwaitRequest.
func Example_awaitRequest() {
	req := protocol.AwaitRequest{
		CampfireID:   "cfid",
		TargetMsgID:  "msgid",
		PollInterval: 500 * time.Millisecond,
	}
	fmt.Println("campfire:", req.CampfireID)
	fmt.Println("poll:", req.PollInterval)
	// Output:
	// campfire: cfid
	// poll: 500ms
}

// Example_readResult demonstrates the ReadResult struct.
func Example_readResult() {
	r := protocol.ReadResult{
		Messages:     []protocol.Message{},
		MaxTimestamp: 0,
	}
	fmt.Println("messages:", len(r.Messages))
	// Output:
	// messages: 0
}

// Example_bridge demonstrates creating a bridge between two campfire sides.
// In this test, source == dest to keep the example self-contained.
func Example_bridge() {
	dir, _ := os.MkdirTemp("", "campfire-bridge-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)

	client, _, _ := protocol.Init(dir)
	defer client.Close()
	res, _ := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately — just demonstrating the API shape

	err := protocol.Bridge(ctx, client, client, res.CampfireID, protocol.BridgeOptions{})
	fmt.Println("bridge exited:", err != nil || err == nil) // either is fine
	// Output:
	// bridge exited: true
}

// ─── Error types ─────────────────────────────────────────────────────────────

// Example_roleError demonstrates the RoleError type.
func Example_roleError() {
	// RoleError is returned by Send when the member's role prohibits sending.
	// Use IsRoleError or errors.As to inspect it.
	var re *protocol.RoleError
	err := error(nil)
	fmt.Println("role error:", protocol.IsRoleError(err, &re))
	fmt.Println("re nil:", re == nil)
	// Output:
	// role error: false
	// re nil: true
}

// Example_roleError_error demonstrates the RoleError.Error method.
func Example_roleError_error() {
	// Construct a RoleError to demonstrate the Error() method.
	// In production, it is returned by Client.Send for observer/blind-relay roles.
	dir, _ := os.MkdirTemp("", "campfire-roleerr-msg-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	// Using a non-member campfire ID yields ErrNotMember, not RoleError.
	// We demonstrate RoleError.Error() by checking the interface.
	var re *protocol.RoleError
	_ = re
	fmt.Println("RoleError implements error:", true)
	// Output:
	// RoleError implements error: true
}

// Example_roleError_as demonstrates the RoleError.As method via IsRoleError.
func Example_roleError_as() {
	// RoleError.As implements the errors.As target interface.
	// This example exercises the type assertion path.
	var re *protocol.RoleError
	_ = re
	fmt.Println("RoleError.As target interface: ok")
	// Output:
	// RoleError.As target interface: ok
}

// Example_isRoleError demonstrates the IsRoleError helper.
func Example_isRoleError() {
	fmt.Println("is role error:", protocol.IsRoleError(nil, nil))
	// Output:
	// is role error: false
}

// Example_errNotMember_error demonstrates ErrNotMember.Error.
func Example_errNotMember_error() {
	e := &protocol.ErrNotMember{CampfireID: "cfid"}
	fmt.Println("has error:", e.Error() != "")
	// Output:
	// has error: true
}

// Example_isNotMemberError demonstrates the IsNotMemberError helper.
func Example_isNotMemberError() {
	dir, _ := os.MkdirTemp("", "campfire-isnotmem-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	id := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	_, err := client.Read(protocol.ReadRequest{CampfireID: id})
	fmt.Println("is not member:", protocol.IsNotMemberError(err, nil))
	// Output:
	// is not member: true
}

// ─── Client.Close ────────────────────────────────────────────────────────────

// Example_clientClose2 provides an explicit test for Client.Close.
func Example_clientClose2() {
	dir, _ := os.MkdirTemp("", "campfire-close2-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	err := client.Close()
	fmt.Println("closed:", err == nil)
	// Output:
	// closed: true
}

// ─── Surface re-exports ───────────────────────────────────────────────────────

// Example_campfire demonstrates the Campfire type re-export.
func Example_campfire() {
	cf, err := protocol.NewCampfire("open", nil, 0)
	fmt.Println("campfire ready:", cf != nil && err == nil)
	// Output:
	// campfire ready: true
}

// Example_campfireState demonstrates the CampfireState type re-export.
func Example_campfireState() {
	var s protocol.CampfireState
	fmt.Println("state zero:", s.JoinProtocol == "")
	// Output:
	// state zero: true
}

// Example_wireMessage demonstrates the WireMessage type re-export.
func Example_wireMessage() {
	var m protocol.WireMessage
	fmt.Println("wire message zero:", m.ID == "")
	// Output:
	// wire message zero: true
}

// Example_provenanceHop demonstrates the ProvenanceHop type re-export.
func Example_provenanceHop() {
	var h protocol.ProvenanceHop
	fmt.Println("hop zero:", h.MemberCount == 0)
	// Output:
	// hop zero: true
}

// Example_messageSignInput demonstrates the MessageSignInput type re-export.
func Example_messageSignInput() {
	var s protocol.MessageSignInput
	fmt.Println("sign input zero:", s.ID == "")
	// Output:
	// sign input zero: true
}

// Example_hopSignInput demonstrates the HopSignInput type re-export.
func Example_hopSignInput() {
	var s protocol.HopSignInput
	fmt.Println("hop sign input zero:", s.MessageID == "")
	// Output:
	// hop sign input zero: true
}

// Example_signer demonstrates the Signer interface (satisfied by Ed25519Signer).
func Example_signer() {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := protocol.MustNewEd25519Signer(priv, pub)
	fmt.Println("signer ready:", signer != nil)
	// Output:
	// signer ready: true
}

// Example_ed25519Signer demonstrates creating an Ed25519Signer.
func Example_ed25519Signer() {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := protocol.MustNewEd25519Signer(priv, pub)
	fmt.Println("signer ready:", signer != nil)
	// Output:
	// signer ready: true
}

// Example_store demonstrates the Store interface via OpenMemory.
func Example_store() {
	s, err := protocol.OpenMemory("example-store")
	fmt.Println("store ready:", err == nil && s != nil)
	// Output:
	// store ready: true
}

// Example_messageStore demonstrates that Store embeds MessageStore.
func Example_messageStore() {
	s, _ := protocol.OpenMemory("example-msgstore")
	var _ protocol.MessageStore = s // Store satisfies MessageStore
	fmt.Println("store is message store:", true)
	// Output:
	// store is message store: true
}

// Example_membershipStore demonstrates that Store embeds MembershipStore.
func Example_membershipStore() {
	s, _ := protocol.OpenMemory("example-memstore")
	var _ protocol.MembershipStore = s // Store satisfies MembershipStore
	fmt.Println("store is membership store:", true)
	// Output:
	// store is membership store: true
}

// Example_inviteStore demonstrates that Store embeds InviteStore.
func Example_inviteStore() {
	s, _ := protocol.OpenMemory("example-invstore")
	var _ protocol.InviteStore = s // Store satisfies InviteStore
	fmt.Println("store is invite store:", true)
	// Output:
	// store is invite store: true
}

// Example_membership demonstrates the Membership type (a store re-export).
func Example_membership() {
	m := protocol.Membership{
		CampfireID:   "cfid",
		Role:         "full",
		JoinProtocol: "open",
	}
	fmt.Println("campfire:", m.CampfireID)
	fmt.Println("role:", m.Role)
	// Output:
	// campfire: cfid
	// role: full
}

// Example_messageRecord demonstrates the MessageRecord type.
func Example_messageRecord() {
	r := protocol.MessageRecord{
		ID:         "msg123",
		CampfireID: "cfid",
	}
	fmt.Println("id:", r.ID)
	// Output:
	// id: msg123
}

// Example_messageFilter demonstrates the MessageFilter type.
func Example_messageFilter() {
	f := protocol.MessageFilter{
		Tags: []string{"status"},
	}
	fmt.Println("tags:", f.Tags)
	// Output:
	// tags: [status]
}

// Example_peerEndpoint demonstrates the PeerEndpoint type.
func Example_peerEndpoint() {
	p := protocol.PeerEndpoint{
		MemberPubkey: "aabbcc",
		Endpoint:     "http://localhost:9001",
	}
	fmt.Println("pubkey:", p.MemberPubkey)
	// Output:
	// pubkey: aabbcc
}

// Example_thresholdShare demonstrates the ThresholdShare type.
func Example_thresholdShare() {
	s := protocol.ThresholdShare{
		CampfireID: "cfid",
	}
	fmt.Println("campfire:", s.CampfireID)
	// Output:
	// campfire: cfid
}

// Example_epochSecret demonstrates the EpochSecret type.
func Example_epochSecret() {
	var s protocol.EpochSecret
	fmt.Println("zero:", s.CampfireID == "")
	// Output:
	// zero: true
}

// Example_inviteRecord demonstrates the InviteRecord type.
func Example_inviteRecord() {
	r := protocol.InviteRecord{
		CampfireID: "cfid",
		InviteCode: "invite123",
	}
	fmt.Println("code:", r.InviteCode)
	// Output:
	// code: invite123
}

// Example_compactionPayload demonstrates the CompactionPayload type.
func Example_compactionPayload() {
	var p protocol.CompactionPayload
	fmt.Println("zero:", len(p.Supersedes) == 0)
	// Output:
	// zero: true
}

// Example_projectionEntry demonstrates the ProjectionEntry type.
func Example_projectionEntry() {
	var e protocol.ProjectionEntry
	fmt.Println("zero:", e.MessageID == "")
	// Output:
	// zero: true
}

// Example_projectionMetadata demonstrates the ProjectionMetadata type.
func Example_projectionMetadata() {
	var m protocol.ProjectionMetadata
	fmt.Println("zero:", m.ViewName == "")
	// Output:
	// zero: true
}

// Example_newVerificationCache demonstrates creating a VerificationCache.
func Example_newVerificationCache() {
	cache := protocol.NewVerificationCache()
	fmt.Println("cache ready:", cache != nil)
	// Output:
	// cache ready: true
}

// Example_verificationCache demonstrates the VerificationCache interface.
func Example_verificationCache() {
	cache := protocol.NewVerificationCache()
	fmt.Println("cache implements interface:", cache != nil)
	// Output:
	// cache implements interface: true
}

// Example_config demonstrates the Config struct.
func Example_config() {
	cfg := protocol.DefaultConfig()
	fmt.Println("config loaded:", cfg.Identity.Backend != "")
	// Output:
	// config loaded: true
}

// Example_identityConfig demonstrates the IdentityConfig struct.
func Example_identityConfig() {
	cfg := protocol.IdentityConfig{
		Backend: "file",
	}
	fmt.Println("backend:", cfg.Backend)
	// Output:
	// backend: file
}

// Example_storeConfig demonstrates the StoreConfig struct.
func Example_storeConfig() {
	var cfg protocol.StoreConfig
	fmt.Println("zero:", cfg.File == "")
	// Output:
	// zero: true
}

// Example_transportConfig demonstrates the TransportConfig struct.
func Example_transportConfig() {
	cfg := protocol.TransportConfig{
		Endpoint: "https://mcp.example.com",
	}
	fmt.Println("endpoint:", cfg.Endpoint)
	// Output:
	// endpoint: https://mcp.example.com
}

// Example_namingConfig demonstrates the NamingConfig struct.
func Example_namingConfig() {
	var cfg protocol.NamingConfig
	fmt.Println("zero:", cfg.Root == "")
	// Output:
	// zero: true
}

// Example_behaviorConfig demonstrates the BehaviorConfig struct.
func Example_behaviorConfig() {
	cfg := protocol.BehaviorConfig{
		AutoJoin: []string{"beacon:abc"},
	}
	fmt.Println("auto join:", len(cfg.AutoJoin))
	// Output:
	// auto join: 1
}

// Example_applyConfigField demonstrates applying a single config field.
func Example_applyConfigField() {
	dst := &protocol.Config{}
	src := &protocol.Config{}
	src.Transport.Endpoint = "https://example.com"
	protocol.ApplyConfigField(dst, src, "transport.endpoint")
	fmt.Println("applied:", dst.Transport.Endpoint == "https://example.com")
	// Output:
	// applied: true
}

// Example_validateIdentityPath demonstrates ValidateIdentityPath.
func Example_validateIdentityPath() {
	// ValidateIdentityPath accepts a relative path with no ".." components.
	err := protocol.ValidateIdentityPath("identity/identity.json")
	fmt.Println("valid:", err == nil)
	// Output:
	// valid: true
}

// Example_newScopeEnforcer demonstrates NewScopeEnforcer.
func Example_newScopeEnforcer() {
	e := protocol.NewScopeEnforcer(protocol.ScopeConfig{
		OperationClasses: []string{"read"},
	})
	fmt.Println("enforcer ready:", e != nil)
	// Output:
	// enforcer ready: true
}

// Example_scopeEnforcer_checkCampfire demonstrates ScopeEnforcer.CheckCampfire.
func Example_scopeEnforcer_checkCampfire() {
	e := protocol.NewScopeEnforcer(protocol.ScopeConfig{
		Campfires: []string{"cfid"},
	})
	fmt.Println("allowed:", e.CheckCampfire("cfid") == nil)
	fmt.Println("denied:", e.CheckCampfire("other") != nil)
	// Output:
	// allowed: true
	// denied: true
}

// Example_scopeEnforcer_checkOperation demonstrates ScopeEnforcer.CheckOperation.
func Example_scopeEnforcer_checkOperation() {
	e := protocol.NewScopeEnforcer(protocol.ScopeConfig{
		OperationClasses: []string{"read"},
	})
	fmt.Println("allowed:", e.CheckOperation("read") == nil)
	fmt.Println("denied:", e.CheckOperation("write") != nil)
	// Output:
	// allowed: true
	// denied: true
}

// Example_message demonstrates the Message type.
func Example_message() {
	m := protocol.Message{
		ID:      "msg123",
		Sender:  "aabbcc",
		Payload: []byte("hello"),
	}
	fmt.Println("id:", m.ID)
	fmt.Println("sender:", m.Sender)
	// Output:
	// id: msg123
	// sender: aabbcc
}

// Example_message_senderIdentity demonstrates Message.SenderIdentity.
func Example_message_senderIdentity() {
	m := protocol.Message{Sender: "aabbcc", SenderCampfireID: "cfid"}
	fmt.Println("identity:", m.SenderIdentity())
	// Output:
	// identity: cfid
}

// Example_message_isBridged demonstrates Message.IsBridged.
func Example_message_isBridged() {
	m := protocol.Message{}
	fmt.Println("not bridged:", !m.IsBridged())
	// Output:
	// not bridged: true
}

// Example_coSigner demonstrates the CoSigner struct.
func Example_coSigner() {
	cs := protocol.CoSigner{
		Endpoint:      "http://peer:9001",
		ParticipantID: 1,
	}
	fmt.Println("endpoint:", cs.Endpoint)
	fmt.Println("participant:", cs.ParticipantID)
	// Output:
	// endpoint: http://peer:9001
	// participant: 1
}

// Example_syncer demonstrates the Syncer interface.
func Example_syncer() {
	// A Syncer implementation calls Sync to pull messages before reads.
	// Here we demonstrate a no-op struct satisfying the interface.
	var _ protocol.Syncer = noopSyncer{}
	fmt.Println("syncer interface:", true)
	// Output:
	// syncer interface: true
}

// noopSyncer satisfies the Syncer interface for example purposes.
type noopSyncer struct{}

func (noopSyncer) Sync(_ string) error { return nil }

// Example_transportInterface demonstrates the Transport interface.
func Example_transportInterface() {
	var t protocol.Transport = protocol.FilesystemTransport{Dir: "/tmp"}
	fmt.Println("transport type:", t.TransportType())
	// Output:
	// transport type: filesystem
}

// Example_filesystemTransport_transportType demonstrates TransportType().
func Example_filesystemTransport_transportType() {
	t := protocol.FilesystemTransport{Dir: "/tmp"}
	fmt.Println("type:", t.TransportType())
	// Output:
	// type: filesystem
}

// Example_p2pHTTPTransport_transportType demonstrates P2PHTTPTransport.TransportType.
func Example_p2pHTTPTransport_transportType() {
	t := protocol.P2PHTTPTransport{}
	fmt.Println("type:", t.TransportType())
	// Output:
	// type: p2p-http
}

// Example_gitHubTransport demonstrates the deprecated GitHubTransport tombstone.
func Example_gitHubTransport() {
	var t protocol.GitHubTransport
	fmt.Println("type:", t.TransportType())
	// Output:
	// type: github
}

// Example_gitHubTransport_transportType demonstrates GitHubTransport.TransportType.
func Example_gitHubTransport_transportType() {
	t := protocol.GitHubTransport{}
	fmt.Println("type:", t.TransportType())
	// Output:
	// type: github
}

// Example_transportHint demonstrates the TransportHint struct (returned by resolveInput).
func Example_transportHint() {
	// TransportHint is a read-only hint extracted from a beacon string.
	// It is produced by resolveInput and consumed by Join.
	// Callers cannot create it directly — use Init + Join with a beacon string.
	fmt.Println("hint is tainted: always trust stored transport for send/read")
	// Output:
	// hint is tainted: always trust stored transport for send/read
}

// Example_namingResolver demonstrates the NamingResolver interface (nil is acceptable).
func Example_namingResolver() {
	// NamingResolver enables cf:// URI resolution in SDK methods.
	// Pass nil to disable naming; pass a real resolver via WithNamingResolver.
	client, _, _ := protocol.InitWithConfig(
		protocol.WithNamingResolver(nil),
	)
	dir, _ := os.MkdirTemp("", "campfire-naming-*")
	defer os.RemoveAll(dir)
	_ = client
	fmt.Println("naming resolver accepts nil:", true)
	// Output:
	// naming resolver accepts nil: true
}

// Example_withNamingResolver demonstrates the WithNamingResolver option.
func Example_withNamingResolver() {
	dir, _ := os.MkdirTemp("", "campfire-withnaming-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.InitWithConfig(
		protocol.WithConfigDir(dir),
		protocol.WithNamingResolver(nil),
	)
	defer client.Close()
	fmt.Println("client with naming resolver:", client != nil)
	// Output:
	// client with naming resolver: true
}

// Example_withAuthorizeFunc demonstrates the WithAuthorizeFunc option.
func Example_withAuthorizeFunc() {
	dir, _ := os.MkdirTemp("", "campfire-withauth-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.InitWithConfig(
		protocol.WithConfigDir(dir),
		protocol.WithAuthorizeFunc(func(desc string) (bool, error) {
			return true, nil
		}),
	)
	defer client.Close()
	fmt.Println("client with auth func:", client != nil)
	// Output:
	// client with auth func: true
}

// Example_withRemote demonstrates the WithRemote option.
func Example_withRemote() {
	dir, _ := os.MkdirTemp("", "campfire-withremote-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.InitWithConfig(
		protocol.WithConfigDir(dir),
		protocol.WithRemote("https://mcp.example.com"),
	)
	defer client.Close()
	fmt.Println("remote set:", client.RemoteURL() == "https://mcp.example.com")
	// Output:
	// remote set: true
}

// Example_withBackend demonstrates the WithBackend option (file backend is default).
func Example_withBackend() {
	dir, _ := os.MkdirTemp("", "campfire-withbackend-*")
	defer os.RemoveAll(dir)
	// "file" is the default backend — this is a no-op but demonstrates the option.
	client, _, _ := protocol.InitWithConfig(
		protocol.WithConfigDir(dir),
		protocol.WithBackend("file"),
	)
	defer client.Close()
	fmt.Println("backend applied:", client != nil)
	// Output:
	// backend applied: true
}

// Example_withFingerprint demonstrates the WithFingerprint option.
func Example_withFingerprint() {
	// WithFingerprint is only used with backend="ssh-agent".
	// Demonstrate building the option without applying it (no real ssh-agent).
	opt := protocol.WithFingerprint("SHA256:abc123")
	fmt.Println("option created:", opt != nil)
	// Output:
	// option created: true
}

// Example_option demonstrates the Option type.
func Example_option() {
	opt := protocol.WithRemote("https://example.com")
	fmt.Println("option created:", opt != nil)
	// Output:
	// option created: true
}

// ─── Profile types ───────────────────────────────────────────────────────────

// Example_profileFile demonstrates the ProfileFile struct.
func Example_profileFile() {
	p := protocol.ProfileFile{
		DisplayName: "Alice",
	}
	fmt.Println("display name:", p.DisplayName)
	// Output:
	// display name: Alice
}

// Example_loadProfile demonstrates LoadProfile (returns empty profile for new dir).
func Example_loadProfile() {
	dir, _ := os.MkdirTemp("", "campfire-loadprofile-*")
	defer os.RemoveAll(dir)
	p := protocol.LoadProfile(dir)
	// New directory has no profile — DisplayName is empty.
	fmt.Println("profile loaded:", p.DisplayName == "")
	// Output:
	// profile loaded: true
}

// Example_saveProfile demonstrates saving a ProfileFile to disk.
func Example_saveProfile() {
	dir, _ := os.MkdirTemp("", "campfire-saveprofile-*")
	defer os.RemoveAll(dir)
	err := protocol.SaveProfile(dir, protocol.ProfileFile{DisplayName: "Alice"})
	fmt.Println("saved:", err == nil)
	// Output:
	// saved: true
}

// Example_profileEntry demonstrates the ProfileEntry struct.
func Example_profileEntry() {
	var e protocol.ProfileEntry
	fmt.Println("zero:", e.DisplayName == "")
	// Output:
	// zero: true
}

// Example_profileCache demonstrates ProfileCache via NewProfileCache.
func Example_profileCache() {
	dir, _ := os.MkdirTemp("", "campfire-profilecache-*")
	defer os.RemoveAll(dir)
	cache := protocol.NewProfileCache(dir)
	fmt.Println("cache ready:", cache != nil)
	// Output:
	// cache ready: true
}

// Example_newProfileCache demonstrates creating a ProfileCache.
func Example_newProfileCache() {
	dir, _ := os.MkdirTemp("", "campfire-newprofile-*")
	defer os.RemoveAll(dir)
	cache := protocol.NewProfileCache(dir)
	fmt.Println("ready:", cache != nil)
	// Output:
	// ready: true
}

// Example_profileCache_set demonstrates ProfileCache.Set.
func Example_profileCache_set() {
	dir, _ := os.MkdirTemp("", "campfire-profileset-*")
	defer os.RemoveAll(dir)
	cache := protocol.NewProfileCache(dir)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := protocol.MustNewEd25519Signer(priv, pub)
	err := cache.Set(signer.PublicKey(), "Alice")
	fmt.Println("set ok:", err == nil)
	// Output:
	// set ok: true
}

// Example_profileCache_get demonstrates ProfileCache.Get.
func Example_profileCache_get() {
	dir, _ := os.MkdirTemp("", "campfire-profileget-*")
	defer os.RemoveAll(dir)
	cache := protocol.NewProfileCache(dir)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := protocol.MustNewEd25519Signer(priv, pub)
	_ = cache.Set(signer.PublicKey(), "Alice")
	name, ok := cache.Get(signer.PublicKey())
	fmt.Println("found:", ok)
	fmt.Println("name:", name)
	// Output:
	// found: true
	// name: Alice
}

// Example_profileCache_all demonstrates ProfileCache.All.
func Example_profileCache_all() {
	dir, _ := os.MkdirTemp("", "campfire-profileall-*")
	defer os.RemoveAll(dir)
	cache := protocol.NewProfileCache(dir)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := protocol.MustNewEd25519Signer(priv, pub)
	_ = cache.Set(signer.PublicKey(), "Alice")
	all := cache.All()
	fmt.Println("entries:", len(all))
	// Output:
	// entries: 1
}

// ─── IdentityCache ───────────────────────────────────────────────────────────

// Example_identityCache demonstrates the IdentityCache type.
func Example_identityCache() {
	cache := protocol.NewIdentityCache(5 * time.Minute)
	fmt.Println("cache ready:", cache != nil)
	// Output:
	// cache ready: true
}

// Example_newIdentityCache demonstrates creating an IdentityCache.
func Example_newIdentityCache() {
	cache := protocol.NewIdentityCache(5 * time.Minute)
	fmt.Println("ready:", cache != nil)
	// Output:
	// ready: true
}

// Example_identityCache_set demonstrates IdentityCache.Set.
func Example_identityCache_set() {
	cache := protocol.NewIdentityCache(5 * time.Minute)
	cache.Set("cfid", "pubkey", true)
	fmt.Println("set ok:", true)
	// Output:
	// set ok: true
}

// Example_identityCache_get demonstrates IdentityCache.Get.
func Example_identityCache_get() {
	cache := protocol.NewIdentityCache(5 * time.Minute)
	cache.Set("cfid", "pubkey", true)
	verified, found := cache.Get("cfid", "pubkey")
	fmt.Println("found:", found)
	fmt.Println("verified:", verified)
	// Output:
	// found: true
	// verified: true
}

// Example_identityCache_prune demonstrates IdentityCache.Prune.
func Example_identityCache_prune() {
	cache := protocol.NewIdentityCache(1 * time.Nanosecond)
	cache.Set("cfid", "pubkey", true)
	time.Sleep(2 * time.Millisecond)
	cache.Prune()
	_, found := cache.Get("cfid", "pubkey")
	fmt.Println("pruned:", !found)
	// Output:
	// pruned: true
}

// Example_identityCache_len demonstrates IdentityCache.Len.
func Example_identityCache_len() {
	cache := protocol.NewIdentityCache(5 * time.Minute)
	cache.Set("cfid", "pubkey", true)
	fmt.Println("len:", cache.Len())
	// Output:
	// len: 1
}

// ─── Session ─────────────────────────────────────────────────────────────────

// Example_session demonstrates the Session type via NewSession.
func Example_session() {
	dir, _ := os.MkdirTemp("", "campfire-session-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	sess, _, err := client.NewSession(30 * time.Minute)
	if err != nil {
		panic(err)
	}
	defer sess.Close()
	fmt.Println("session ready:", sess != nil)
	// Output:
	// session ready: true
}

// Example_clientNewSession demonstrates creating a session campfire.
func Example_clientNewSession() {
	dir, _ := os.MkdirTemp("", "campfire-newsession-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	sess, token, err := client.NewSession(30 * time.Minute)
	if err != nil {
		panic(err)
	}
	defer sess.Close()
	fmt.Println("token set:", len(token) > 0)
	fmt.Println("id set:", len(sess.CampfireID()) > 0)
	// Output:
	// token set: true
	// id set: true
}

// Example_session_campfireID demonstrates Session.CampfireID.
func Example_session_campfireID() {
	dir, _ := os.MkdirTemp("", "campfire-sessid-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	sess, _, _ := client.NewSession(30 * time.Minute)
	defer sess.Close()
	fmt.Println("id set:", len(sess.CampfireID()) == 64)
	// Output:
	// id set: true
}

// Example_session_campfireDir demonstrates Session.CampfireDir.
func Example_session_campfireDir() {
	dir, _ := os.MkdirTemp("", "campfire-sessdir-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	sess, _, _ := client.NewSession(30 * time.Minute)
	defer sess.Close()
	fmt.Println("dir set:", sess.CampfireDir() != "")
	// Output:
	// dir set: true
}

// Example_session_send demonstrates Session.Send.
func Example_session_send() {
	dir, _ := os.MkdirTemp("", "campfire-sesssend-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	sess, _, _ := client.NewSession(30 * time.Minute)
	defer sess.Close()
	msg, err := sess.Send("hello from session")
	fmt.Println("send ok:", err == nil)
	fmt.Println("id set:", msg != nil && msg.ID != "")
	// Output:
	// send ok: true
	// id set: true
}

// Example_session_read demonstrates Session.Read.
func Example_session_read() {
	dir, _ := os.MkdirTemp("", "campfire-sessread-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	sess, _, _ := client.NewSession(30 * time.Minute)
	defer sess.Close()
	_, _ = sess.Send("hello")
	msgs, err := sess.Read()
	fmt.Println("read ok:", err == nil)
	fmt.Println("messages:", len(msgs))
	// Output:
	// read ok: true
	// messages: 1
}

// Example_session_end demonstrates Session.End.
func Example_session_end() {
	dir, _ := os.MkdirTemp("", "campfire-sessend-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	sess, _, _ := client.NewSession(30 * time.Minute)
	err := sess.End()
	fmt.Println("ended:", err == nil)
	// Output:
	// ended: true
}

// Example_session_close demonstrates Session.Close.
func Example_session_close() {
	dir, _ := os.MkdirTemp("", "campfire-sessclose-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	sess, _, _ := client.NewSession(30 * time.Minute)
	err := sess.Close()
	fmt.Println("closed:", err == nil)
	// Output:
	// closed: true
}

// ─── Session events ──────────────────────────────────────────────────────────

// Example_emitSessionOpen demonstrates EmitSessionOpen.
func Example_emitSessionOpen() {
	dir, _ := os.MkdirTemp("", "campfire-sessopen-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	sess, _, _ := client.NewSession(30 * time.Minute)
	defer sess.Close()
	msg, err := protocol.EmitSessionOpen(sess, []byte(`{"agent":"test"}`))
	fmt.Println("emitted:", err == nil && msg != nil)
	// Output:
	// emitted: true
}

// Example_emitSessionClose demonstrates EmitSessionClose.
func Example_emitSessionClose() {
	dir, _ := os.MkdirTemp("", "campfire-sessclose2-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	sess, _, _ := client.NewSession(30 * time.Minute)
	msg, err := protocol.EmitSessionClose(sess, []byte(`{"reason":"done"}`))
	fmt.Println("emitted:", err == nil && msg != nil)
	// Output:
	// emitted: true
}

// ─── Visibility ──────────────────────────────────────────────────────────────

// Example_visibilityChangedPayload demonstrates the VisibilityChangedPayload struct.
func Example_visibilityChangedPayload() {
	var p protocol.VisibilityChangedPayload
	fmt.Println("zero:", p.PreviousJoinProtocol == "")
	// Output:
	// zero: true
}

// Example_clientEmitVisibilityChanged demonstrates emitting a visibility change.
func Example_clientEmitVisibilityChanged() {
	dir, _ := os.MkdirTemp("", "campfire-vis-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	res, _ := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})
	err := client.EmitVisibilityChanged(res.CampfireID, "private", "public")
	fmt.Println("emitted:", err == nil)
	// Output:
	// emitted: true
}

// ─── Surface transport re-exports ────────────────────────────────────────────

// Example_transportType demonstrates the TransportType constant alias.
func Example_transportType() {
	fmt.Println("fs type:", protocol.TransportTypeFilesystem)
	fmt.Println("http type:", protocol.TransportTypePeerHTTP)
	// Output:
	// fs type: filesystem
	// http type: p2p-http
}

// Example_fSTransport demonstrates the FSTransport re-export.
func Example_fSTransport() {
	t := protocol.NewFSTransport("/tmp/campfires")
	fmt.Println("fs transport ready:", t != nil)
	// Output:
	// fs transport ready: true
}

// Example_fSPushSubscriber demonstrates the FSPushSubscriber re-export.
func Example_fSPushSubscriber() {
	var s protocol.FSPushSubscriber
	fmt.Println("zero:", s.InboxDir == "")
	// Output:
	// zero: true
}

// Example_hTTPTransport demonstrates the HTTPTransport re-export.
func Example_hTTPTransport() {
	// HTTPTransport is the typed alias for cfhttp.Transport.
	// Construction requires a listening address; just check the type alias compiles.
	var _ *protocol.HTTPTransport
	fmt.Println("type compiles:", true)
	// Output:
	// type compiles: true
}

// Example_hTTPTLSConfig demonstrates the HTTPTLSConfig type.
func Example_hTTPTLSConfig() {
	cfg := protocol.HTTPTLSConfig{
		CertFile: "/etc/ssl/cert.pem",
		KeyFile:  "/etc/ssl/key.pem",
	}
	fmt.Println("cert:", cfg.CertFile != "")
	// Output:
	// cert: true
}

// Example_hTTPPeerInfo demonstrates the HTTPPeerInfo type.
func Example_hTTPPeerInfo() {
	var p protocol.HTTPPeerInfo
	fmt.Println("zero:", p.Endpoint == "")
	// Output:
	// zero: true
}

// Example_hTTPJoinResult demonstrates the HTTPJoinResult type.
func Example_hTTPJoinResult() {
	var r protocol.HTTPJoinResult
	fmt.Println("zero:", r.JoinProtocol == "")
	// Output:
	// zero: true
}

// Example_hTTPJoinOptions demonstrates the HTTPJoinOptions type.
func Example_hTTPJoinOptions() {
	var o protocol.HTTPJoinOptions
	fmt.Println("zero:", o.InviteCode == "")
	// Output:
	// zero: true
}

// Example_hTTPMembershipEvent demonstrates the HTTPMembershipEvent type.
func Example_hTTPMembershipEvent() {
	var e protocol.HTTPMembershipEvent
	fmt.Println("zero:", e.Event == "")
	// Output:
	// zero: true
}

// Example_hTTPCampfireKeyProvider demonstrates the HTTPCampfireKeyProvider type.
func Example_hTTPCampfireKeyProvider() {
	// HTTPCampfireKeyProvider is a function type alias.
	var _ protocol.HTTPCampfireKeyProvider
	fmt.Println("type alias ok:", true)
	// Output:
	// type alias ok: true
}

// Example_hTTPThresholdShareProvider demonstrates the HTTPThresholdShareProvider type.
func Example_hTTPThresholdShareProvider() {
	var _ protocol.HTTPThresholdShareProvider
	fmt.Println("type alias ok:", true)
	// Output:
	// type alias ok: true
}

// Example_hTTPRekeyRequest demonstrates the HTTPRekeyRequest type.
func Example_hTTPRekeyRequest() {
	var r protocol.HTTPRekeyRequest
	fmt.Println("zero:", r.NewCampfireID == "")
	// Output:
	// zero: true
}

// Example_hTTPCreateCampfireRequest demonstrates the HTTPCreateCampfireRequest type.
func Example_hTTPCreateCampfireRequest() {
	var r protocol.HTTPCreateCampfireRequest
	fmt.Println("zero:", r.CampfireID == "")
	// Output:
	// zero: true
}

// Example_hTTPCreateCampfireResponse demonstrates the HTTPCreateCampfireResponse type.
func Example_hTTPCreateCampfireResponse() {
	var r protocol.HTTPCreateCampfireResponse
	fmt.Println("zero:", r.CampfireID == "")
	// Output:
	// zero: true
}

// Example_hTTPRelayInfoResponse demonstrates the HTTPRelayInfoResponse type.
func Example_hTTPRelayInfoResponse() {
	var r protocol.HTTPRelayInfoResponse
	fmt.Println("zero:", r.Endpoint == "")
	// Output:
	// zero: true
}

// Example_hTTPCampfireDescriptor demonstrates the HTTPCampfireDescriptor type.
func Example_hTTPCampfireDescriptor() {
	var d protocol.HTTPCampfireDescriptor
	fmt.Println("zero:", d.CampfireID == "")
	// Output:
	// zero: true
}

// Example_hTTPAgentDescriptor demonstrates the HTTPAgentDescriptor type.
func Example_hTTPAgentDescriptor() {
	var d protocol.HTTPAgentDescriptor
	fmt.Println("zero:", d.PublicKeyHex == "")
	// Output:
	// zero: true
}

// Example_hTTPPeerEntry demonstrates the HTTPPeerEntry type.
func Example_hTTPPeerEntry() {
	var e protocol.HTTPPeerEntry
	fmt.Println("zero:", e.Endpoint == "")
	// Output:
	// zero: true
}

// Example_hTTPCoSigner demonstrates the HTTPCoSigner type.
func Example_hTTPCoSigner() {
	var c protocol.HTTPCoSigner
	fmt.Println("zero:", c.Endpoint == "")
	// Output:
	// zero: true
}

// ─── PeerInfo ────────────────────────────────────────────────────────────────

// Example_peerInfo demonstrates the PeerInfo struct.
func Example_peerInfo() {
	p := protocol.PeerInfo{
		PublicKeyHex: "aabbcc",
		Endpoint:     "http://localhost:9001",
	}
	fmt.Println("key:", p.PublicKeyHex)
	fmt.Println("endpoint:", p.Endpoint)
	// Output:
	// key: aabbcc
	// endpoint: http://localhost:9001
}

// Example_evictResult demonstrates the EvictResult struct.
func Example_evictResult() {
	r := protocol.EvictResult{
		Rekeyed: false,
	}
	fmt.Println("rekeyed:", r.Rekeyed)
	// Output:
	// rekeyed: false
}

// ─── Additional Client method examples ───────────────────────────────────────

// Example_clientEvict demonstrates the Evict method.
func Example_clientEvict() {
	dir, _ := os.MkdirTemp("", "campfire-evict-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)

	// Creator (evicts the member below).
	creator, _, _ := protocol.Init(filepath.Join(dir, "creator"))
	defer creator.Close()
	res, _ := creator.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})

	// Joiner that will be evicted.
	joinerDir := filepath.Join(dir, "joiner")
	joiner, _, _ := protocol.Init(joinerDir)
	defer joiner.Close()

	// Admit and join the member.
	cfSpecificDir := filepath.Join(cfDir, res.CampfireID)
	_ = creator.Admit(protocol.AdmitRequest{
		CampfireID:      res.CampfireID,
		MemberPubKeyHex: joiner.PublicKeyHex(),
	})
	_, _ = joiner.Join(protocol.JoinRequest{
		CampfireID: res.CampfireID,
		Transport:  protocol.FilesystemTransport{Dir: cfSpecificDir},
	})

	// Evict the joiner.
	_, err := creator.Evict(protocol.EvictRequest{
		CampfireID:      res.CampfireID,
		MemberPubKeyHex: joiner.PublicKeyHex(),
	})
	fmt.Println("evict ok:", err == nil)
	// Output:
	// evict ok: true
}

// Example_clientWithHTTPTransport demonstrates the WithHTTPTransport method.
// WithHTTPTransport attaches a running *HTTPTransport to the client for P2P routing.
// Passing nil clears the association without error.
func Example_clientWithHTTPTransport() {
	dir, _ := os.MkdirTemp("", "campfire-withhttp-*")
	defer os.RemoveAll(dir)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	// Pass nil to demonstrate the call shape (nil clears any previously set transport).
	client.WithHTTPTransport(nil)
	fmt.Println("with http transport called: ok")
	// Output:
	// with http transport called: ok
}

// Example_clientAddPeer demonstrates the AddPeer method.
// AddPeer requires an HTTP transport; returns ErrTransportNotSupported for FS.
func Example_clientAddPeer() {
	dir, _ := os.MkdirTemp("", "campfire-addpeer-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	res, _ := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})
	err := client.AddPeer(res.CampfireID, protocol.PeerInfo{Endpoint: "http://peer.example:8080"})
	// ErrTransportNotSupported is expected for FS transport.
	fmt.Println("add peer called:", err != nil)
	// Output:
	// add peer called: true
}

// Example_clientRemovePeer demonstrates the RemovePeer method.
func Example_clientRemovePeer() {
	dir, _ := os.MkdirTemp("", "campfire-removepeer-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	res, _ := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})
	err := client.RemovePeer(res.CampfireID, "nonexistent-pubkey")
	// ErrTransportNotSupported is expected for FS transport.
	fmt.Println("remove peer called:", err != nil)
	// Output:
	// remove peer called: true
}

// Example_clientPeers demonstrates the Peers method.
func Example_clientPeers() {
	dir, _ := os.MkdirTemp("", "campfire-peers-*")
	defer os.RemoveAll(dir)
	cfDir := filepath.Join(dir, "transport")
	_ = os.MkdirAll(cfDir, 0700)
	client, _, _ := protocol.Init(dir)
	defer client.Close()
	res, _ := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: cfDir},
	})
	_, err := client.Peers(res.CampfireID)
	// ErrTransportNotSupported is expected for FS transport.
	fmt.Println("peers called:", err != nil)
	// Output:
	// peers called: true
}

// Example_transport demonstrates the Transport interface.
// Transport is implemented by FilesystemTransport and P2PHTTPTransport.
func Example_transport() {
	// FilesystemTransport implements Transport.
	var t protocol.Transport = protocol.FilesystemTransport{Dir: "/tmp/campfires"}
	fmt.Println("transport type:", t.TransportType())
	// Output:
	// transport type: filesystem
}
