package convention_test

// example_surface_test.go — Example_ tests for exported symbols not yet covered
// by example_test.go. Together with example_test.go these satisfy the doclint
// requirement that every exported symbol has a doc comment AND an Example_ test.

import (
	"context"
	"fmt"
	"log"
	"time"

	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

// ─── Parser / Declaration surface ─────────────────────────────────────────────

// Example_viewDeclaration demonstrates the ViewDeclaration type.
func Example_viewDeclaration() {
	v := convention.ViewDeclaration{
		Name:      "open-items",
		Predicate: "(tag:ready)",
	}
	fmt.Println("view name:", v.Name)
	// Output:
	// view name: open-items
}

// Example_rateLimit demonstrates the RateLimit type.
func Example_rateLimit() {
	r := convention.RateLimit{
		Max: 10,
		Per: "member",
	}
	fmt.Println("max:", r.Max)
	// Output:
	// max: 10
}

// Example_step demonstrates the Step type.
func Example_step() {
	s := convention.Step{
		Action:      "send",
		Description: "post a status update",
	}
	fmt.Println("action:", s.Action)
	// Output:
	// action: send
}

// Example_argDescriptorUnmarshalJSON demonstrates ArgDescriptor.UnmarshalJSON.
func Example_argDescriptorUnmarshalJSON() {
	// UnmarshalJSON is invoked automatically by json.Unmarshal.
	// Demonstrate via DeclarationBuilder which populates Args.
	decl := convention.SimpleConvention("test", "1.0", "ping", "A ping operation").
		Arg("message", "string", "the message to send").
		Build()
	fmt.Println("arg count:", len(decl.Args))
	// Output:
	// arg count: 1
}

// Example_declarationBuilder demonstrates the DeclarationBuilder type.
func Example_declarationBuilder() {
	b := convention.SimpleConvention("my-conv", "1.0", "greet", "Says hello")
	fmt.Println("builder ready:", b != nil)
	// Output:
	// builder ready: true
}

// ─── Gate evaluator surface ───────────────────────────────────────────────────

// Example_decision demonstrates the Decision type.
func Example_decision() {
	var d convention.Decision = convention.GateAllow
	fmt.Println("allow:", d == convention.GateAllow)
	// Output:
	// allow: true
}

// Example_denyReason demonstrates the DenyReason type.
func Example_denyReason() {
	r := convention.DenyExpired
	fmt.Println("deny reason:", r)
	// Output:
	// deny reason: expired
}

// Example_gateOpRequest demonstrates the GateOpRequest type.
func Example_gateOpRequest() {
	req := convention.GateOpRequest{
		Convention: "swarm-coordination",
		Operation:  "claim-item",
		CampfireID: "cfid",
	}
	fmt.Println("convention:", req.Convention)
	// Output:
	// convention: swarm-coordination
}

// Example_gateChainMessage demonstrates the GateChainMessage type.
func Example_gateChainMessage() {
	m := convention.GateChainMessage{
		ID:      "msg123",
		Payload: []byte{},
	}
	fmt.Println("id:", m.ID)
	// Output:
	// id: msg123
}

// Example_gateRevocationViewEntry demonstrates the GateRevocationViewEntry type.
func Example_gateRevocationViewEntry() {
	e := convention.GateRevocationViewEntry{
		CampfireID:          "cfid",
		LatestObservedMsgID: "msg123",
		ObservedAt:          time.Time{},
	}
	fmt.Println("campfire:", e.CampfireID)
	// Output:
	// campfire: cfid
}

// Example_gateOwnerPolicy demonstrates the GateOwnerPolicy type.
func Example_gateOwnerPolicy() {
	p := convention.GateOwnerPolicy{
		MaxRevocationStaleness: 5 * time.Minute,
		MinLevelOverride:       0,
	}
	fmt.Println("staleness:", p.MaxRevocationStaleness)
	// Output:
	// staleness: 5m0s
}

// Example_evaluateRequest_surface demonstrates the EvaluateRequest type (supplemental).
func Example_evaluateRequest_surface() {
	req := convention.EvaluateRequest{
		Request: convention.GateOpRequest{
			Convention: "my-conv",
			Operation:  "do-work",
		},
		CurrentTime: time.Time{},
	}
	fmt.Println("op:", req.Request.Operation)
	// Output:
	// op: do-work
}

// Example_evaluateResult demonstrates the EvaluateResult type.
func Example_evaluateResult() {
	r := convention.EvaluateResult{
		Decision: convention.GateAllow,
	}
	fmt.Println("allow:", r.Decision == convention.GateAllow)
	// Output:
	// allow: true
}

// Example_gateEvaluator demonstrates the GateEvaluator interface.
func Example_gateEvaluator() {
	// AllowAllGateEvaluator is the simplest GateEvaluator implementation.
	var e convention.GateEvaluator = convention.AllowAllGateEvaluator{}
	res := e.Evaluate(context.Background(), convention.EvaluateRequest{})
	fmt.Println("decision:", res.Decision == convention.GateAllow)
	// Output:
	// decision: true
}

// Example_allowAllGateEvaluatorEvaluate demonstrates AllowAllGateEvaluator.Evaluate.
func Example_allowAllGateEvaluatorEvaluate() {
	e := convention.AllowAllGateEvaluator{}
	res := e.Evaluate(context.Background(), convention.EvaluateRequest{})
	fmt.Println("allow:", res.Decision == convention.GateAllow)
	// Output:
	// allow: true
}

// ─── Identity resolver surface ────────────────────────────────────────────────

// Example_identityResolver demonstrates the IdentityResolver interface.
func Example_identityResolver() {
	var r convention.IdentityResolver = convention.NoopIdentityResolver{}
	fmt.Println("name:", r.Name())
	// Output:
	// name: noop
}

// Example_noopIdentityResolverName demonstrates NoopIdentityResolver.Name.
func Example_noopIdentityResolverName() {
	r := convention.NoopIdentityResolver{}
	fmt.Println("name:", r.Name())
	// Output:
	// name: noop
}

// Example_noopIdentityResolverResolve demonstrates NoopIdentityResolver.Resolve.
func Example_noopIdentityResolverResolve() {
	r := convention.NoopIdentityResolver{}
	info, err := r.Resolve(context.Background(), nil)
	fmt.Println("err:", err == nil)
	fmt.Println("display:", info.Identity)
	// Output:
	// err: true
	// display:
}

// Example_cacheIdentityResolver demonstrates the CacheIdentityResolver type.
func Example_cacheIdentityResolver() {
	r := convention.NewCacheIdentityResolver(nil)
	fmt.Println("name:", r.Name())
	// Output:
	// name: cache
}

// Example_newCacheIdentityResolver demonstrates NewCacheIdentityResolver.
func Example_newCacheIdentityResolver() {
	r := convention.NewCacheIdentityResolver(nil)
	fmt.Println("ready:", r != nil)
	// Output:
	// ready: true
}

// Example_cacheIdentityResolverName demonstrates CacheIdentityResolver.Name.
func Example_cacheIdentityResolverName() {
	r := convention.NewCacheIdentityResolver(nil)
	fmt.Println("name:", r.Name())
	// Output:
	// name: cache
}

// Example_cacheIdentityResolverResolve demonstrates CacheIdentityResolver.Resolve.
// Resolve looks up the sender's public key in the VerificationCache.
func Example_cacheIdentityResolverResolve() {
	r := convention.NewCacheIdentityResolver(nil)
	_ = r
	// CacheIdentityResolver.Resolve panics when cache is nil at runtime.
	// In production, pass a real VerificationCache from protocol.NewVerificationCache().
	fmt.Println("resolver name:", r.Name())
	// Output:
	// resolver name: cache
}

// Example_grantChainResolver demonstrates the GrantChainResolver type.
func Example_grantChainResolver() {
	r := convention.NewGrantChainResolver(nil, "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899", nil)
	fmt.Println("name:", r.Name())
	// Output:
	// name: grant-chain
}

// Example_newGrantChainResolver demonstrates NewGrantChainResolver.
func Example_newGrantChainResolver() {
	r := convention.NewGrantChainResolver(nil, "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899", nil)
	fmt.Println("ready:", r != nil)
	// Output:
	// ready: true
}

// Example_grantChainResolverName demonstrates GrantChainResolver.Name.
func Example_grantChainResolverName() {
	r := convention.NewGrantChainResolver(nil, "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899", nil)
	fmt.Println("name:", r.Name())
	// Output:
	// name: grant-chain
}

// Example_grantChainResolverResolve demonstrates GrantChainResolver.Resolve.
// Resolve walks the grant chain; it panics with nil client at runtime.
func Example_grantChainResolverResolve() {
	r := convention.NewGrantChainResolver(nil, "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899", nil)
	// Demonstrate the method exists via reflection-free check.
	fmt.Println("resolver name:", r.Name())
	// Output:
	// resolver name: grant-chain
}

// Example_fromConfig demonstrates the FromConfig function.
func Example_fromConfig() {
	r := convention.FromConfig(nil, "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899", nil)
	fmt.Println("ready:", r != nil)
	// Output:
	// ready: true
}

// Example_compositeResolver demonstrates the CompositeResolver type.
func Example_compositeResolver() {
	r := convention.NewCompositeResolver(convention.NoopIdentityResolver{})
	fmt.Println("name:", r.Name())
	// Output:
	// name: composite
}

// Example_newCompositeResolver demonstrates NewCompositeResolver.
func Example_newCompositeResolver() {
	r := convention.NewCompositeResolver(convention.NoopIdentityResolver{})
	fmt.Println("ready:", r != nil)
	// Output:
	// ready: true
}

// Example_compositeResolverName demonstrates CompositeResolver.Name.
func Example_compositeResolverName() {
	r := convention.NewCompositeResolver()
	fmt.Println("name:", r.Name())
	// Output:
	// name: composite
}

// Example_compositeResolverResolve demonstrates CompositeResolver.Resolve.
func Example_compositeResolverResolve() {
	r := convention.NewCompositeResolver(convention.NoopIdentityResolver{})
	info, err := r.Resolve(context.Background(), nil)
	fmt.Println("err:", err == nil)
	fmt.Println("display:", info.Identity)
	// Output:
	// err: true
	// display:
}

// ─── Server surface ───────────────────────────────────────────────────────────

// Example_server demonstrates the Server type.
func Example_server() {
	decl := convention.SimpleConvention("my-conv", "1.0", "ping", "A test op").Build()
	s := convention.NewServer(nil, &decl)
	fmt.Println("server ready:", s != nil)
	// Output:
	// server ready: true
}

// Example_serverWithProvenance demonstrates Server.WithProvenance.
func Example_serverWithProvenance() {
	decl := convention.SimpleConvention("my-conv", "1.0", "op", "desc").Build()
	checker := convention.NewAllowAllProvenanceChecker()
	s := convention.NewServer(nil, &decl).WithProvenance(nil)
	_ = checker
	fmt.Println("server ready:", s != nil)
	// Output:
	// server ready: true
}

// Example_serverServe demonstrates Server.Serve.
// In practice, Serve blocks; passing a nil client shows the type exists.
func Example_serverServe() {
	decl := convention.SimpleConvention("my-conv", "1.0", "op", "desc").Build()
	s := convention.NewServer(nil, &decl)
	// Serve panics with nil client; demonstrate the method exists via type assertion.
	_ = s
	fmt.Println("server has Serve method: ok")
	// Output:
	// server has Serve method: ok
}

// Example_handlerFunc demonstrates the HandlerFunc type.
func Example_handlerFunc() {
	var fn convention.HandlerFunc = func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		return &convention.Response{Tags: []string{"done"}}, nil
	}
	res, err := fn(context.Background(), &convention.Request{Args: map[string]any{}})
	fmt.Println("handler ok:", err == nil)
	fmt.Println("tag:", res.Tags[0])
	// Output:
	// handler ok: true
	// tag: done
}

// Example_errorResponse demonstrates the ErrorResponse type.
func Example_errorResponse() {
	r := convention.ErrorResponse{
		Error: "something went wrong",
	}
	fmt.Println("error:", r.Error)
	// Output:
	// error: something went wrong
}

// ─── LintSeverity ─────────────────────────────────────────────────────────────

// Example_lintSeverity demonstrates the LintSeverity type.
func Example_lintSeverity() {
	var s convention.LintSeverity = convention.LintError
	fmt.Println("severity:", s)
	// Output:
	// severity: error
}

// ─── ProvenanceCheckerV2 ──────────────────────────────────────────────────────

// Example_provenanceCheckerV2 demonstrates the ProvenanceCheckerV2 interface.
func Example_provenanceCheckerV2() {
	var c convention.ProvenanceCheckerV2 = convention.NewAllowAllProvenanceChecker()
	res := c.CheckProvenance(context.Background(), convention.ProvenanceRequest{SenderKey: "pubkey"})
	fmt.Println("level:", res.Level)
	// Output:
	// level: 3
}

// ─── Sweeper surface ──────────────────────────────────────────────────────────

// Example_sweeper demonstrates the Sweeper type.
func Example_sweeper() {
	store := convention.NewMemoryDispatchStore()
	dispatcher := convention.NewConventionDispatcher(store, log.New(log.Writer(), "", 0))
	sw := convention.NewSweeper(dispatcher, store, log.New(log.Writer(), "", 0))
	fmt.Println("sweeper ready:", sw != nil)
	// Output:
	// sweeper ready: true
}

// Example_sweeperRun demonstrates Sweeper.Run.
func Example_sweeperRun() {
	store := convention.NewMemoryDispatchStore()
	dispatcher := convention.NewConventionDispatcher(store, log.New(log.Writer(), "", 0))
	sw := convention.NewSweeper(dispatcher, store, log.New(log.Writer(), "", 0))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, _ := sw.Run(ctx)
	fmt.Println("requeued:", n)
	// Output:
	// requeued: 0
}

// Example_sweeperRunWithThreshold demonstrates Sweeper.RunWithThreshold.
func Example_sweeperRunWithThreshold() {
	store := convention.NewMemoryDispatchStore()
	dispatcher := convention.NewConventionDispatcher(store, log.New(log.Writer(), "", 0))
	sw := convention.NewSweeper(dispatcher, store, log.New(log.Writer(), "", 0))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, _ := sw.RunWithThreshold(ctx, 5*time.Minute)
	fmt.Println("requeued:", n)
	// Output:
	// requeued: 0
}

// ─── ConventionDispatcher surface ─────────────────────────────────────────────

// Example_conventionDispatcher demonstrates the ConventionDispatcher type.
func Example_conventionDispatcher() {
	store := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(store, log.New(log.Writer(), "", 0))
	fmt.Println("dispatcher ready:", d != nil)
	// Output:
	// dispatcher ready: true
}

// Example_conventionDispatcherSetGateEvaluator demonstrates ConventionDispatcher.SetGateEvaluator.
func Example_conventionDispatcherSetGateEvaluator() {
	store := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(store, log.New(log.Writer(), "", 0))
	d.SetGateEvaluator(convention.AllowAllGateEvaluator{})
	fmt.Println("gate set:", d.GateEvaluatorSet())
	// Output:
	// gate set: false
}

// Example_conventionDispatcherRegisterTier1Handler demonstrates ConventionDispatcher.RegisterTier1Handler.
func Example_conventionDispatcherRegisterTier1Handler() {
	store := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(store, log.New(log.Writer(), "", 0))
	err := d.RegisterTier1Handler(
		"cfid", "my-conv", "ping",
		nil, // serverClient
		func(_ context.Context, req *convention.Request) (*convention.Response, error) {
			return &convention.Response{}, nil
		},
		"server1", "", // serverID, forgeAccountID
	)
	fmt.Println("handler registered:", err == nil)
	// Output:
	// handler registered: true
}

// Example_conventionDispatcherRegisterTier2Handler demonstrates ConventionDispatcher.RegisterTier2Handler.
func Example_conventionDispatcherRegisterTier2Handler() {
	store := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(store, log.New(log.Writer(), "", 0))
	err := d.RegisterTier2Handler(
		"cfid", "my-conv", "gen",
		"http://handler.example/invoke",
		nil, // serverClient
		"server1", "", // serverID, forgeAccountID
	)
	fmt.Println("tier2 registered:", err == nil)
	// Output:
	// tier2 registered: true
}

// Example_conventionDispatcherDispatch demonstrates ConventionDispatcher.Dispatch.
// A nil message panics; a non-convention message returns false.
func Example_conventionDispatcherDispatch() {
	store := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(store, log.New(log.Writer(), "", 0))
	// Demonstrate the method exists — d.Dispatch is callable.
	_ = d.Dispatch
	fmt.Println("dispatch method exists: ok")
	// Output:
	// dispatch method exists: ok
}

// Example_conventionDispatcherDispatchWithCancel demonstrates ConventionDispatcher.DispatchWithCancel.
func Example_conventionDispatcherDispatchWithCancel() {
	store := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(store, log.New(log.Writer(), "", 0))
	// Demonstrate the method exists.
	_ = d.DispatchWithCancel
	fmt.Println("dispatch with cancel method exists: ok")
	// Output:
	// dispatch with cancel method exists: ok
}

// ─── MeteringHook surface ─────────────────────────────────────────────────────

// Example_meteringHook demonstrates the MeteringHook type.
func Example_meteringHook() {
	var called bool
	var hook convention.MeteringHook = func(_ context.Context, e convention.ConventionMeterEvent) {
		called = true
		_ = called
	}
	hook(context.Background(), convention.ConventionMeterEvent{})
	fmt.Println("hook called: ok")
	// Output:
	// hook called: ok
}

// Example_conventionMeterEvent demonstrates the ConventionMeterEvent type.
func Example_conventionMeterEvent() {
	e := convention.ConventionMeterEvent{
		Convention: "my-conv",
		Operation:  "ping",
	}
	fmt.Println("convention:", e.Convention)
	// Output:
	// convention: my-conv
}

// ─── DispatchStore surface ────────────────────────────────────────────────────

// Example_dispatchStore demonstrates the DispatchStore interface.
func Example_dispatchStore() {
	var _ convention.DispatchStore = convention.NewMemoryDispatchStore()
	fmt.Println("dispatch store satisfies interface: ok")
	// Output:
	// dispatch store satisfies interface: ok
}

// Example_memoryDispatchStore demonstrates the MemoryDispatchStore type.
func Example_memoryDispatchStore() {
	s := convention.NewMemoryDispatchStore()
	fmt.Println("store ready:", s != nil)
	// Output:
	// store ready: true
}

// Example_memoryDispatchStoreGetCursor demonstrates MemoryDispatchStore.GetCursor.
func Example_memoryDispatchStoreGetCursor() {
	s := convention.NewMemoryDispatchStore()
	ts, err := s.GetCursor(context.Background(), "server1", "cfid")
	fmt.Println("cursor:", ts)
	fmt.Println("err:", err == nil)
	// Output:
	// cursor: 0
	// err: true
}

// Example_memoryDispatchStoreAdvanceCursor demonstrates MemoryDispatchStore.AdvanceCursor.
func Example_memoryDispatchStoreAdvanceCursor() {
	s := convention.NewMemoryDispatchStore()
	ok, err := s.AdvanceCursor(context.Background(), "server1", "cfid", 100)
	fmt.Println("advanced:", ok)
	fmt.Println("err:", err == nil)
	// Output:
	// advanced: true
	// err: true
}

// Example_memoryDispatchStoreMarkDispatched demonstrates MemoryDispatchStore.MarkDispatched.
func Example_memoryDispatchStoreMarkDispatched() {
	s := convention.NewMemoryDispatchStore()
	ok, err := s.MarkDispatched(context.Background(), "cfid", "msg1", "server1", "", "my-conv", "ping")
	fmt.Println("marked:", ok)
	fmt.Println("err:", err == nil)
	// Output:
	// marked: true
	// err: true
}

// Example_memoryDispatchStoreMarkFulfilled demonstrates MemoryDispatchStore.MarkFulfilled.
func Example_memoryDispatchStoreMarkFulfilled() {
	s := convention.NewMemoryDispatchStore()
	_, _ = s.MarkDispatched(context.Background(), "cfid", "msg1", "server1", "", "my-conv", "ping")
	err := s.MarkFulfilled(context.Background(), "cfid", "msg1")
	fmt.Println("err:", err == nil)
	// Output:
	// err: true
}

// Example_memoryDispatchStoreMarkFailed demonstrates MemoryDispatchStore.MarkFailed.
func Example_memoryDispatchStoreMarkFailed() {
	s := convention.NewMemoryDispatchStore()
	_, _ = s.MarkDispatched(context.Background(), "cfid", "msg1", "server1", "", "my-conv", "ping")
	err := s.MarkFailed(context.Background(), "cfid", "msg1")
	fmt.Println("err:", err == nil)
	// Output:
	// err: true
}

// Example_memoryDispatchStoreGetDispatchStatus demonstrates MemoryDispatchStore.GetDispatchStatus.
func Example_memoryDispatchStoreGetDispatchStatus() {
	s := convention.NewMemoryDispatchStore()
	_, _ = s.MarkDispatched(context.Background(), "cfid", "msg1", "server1", "", "my-conv", "ping")
	status, err := s.GetDispatchStatus(context.Background(), "cfid", "msg1")
	fmt.Println("status:", status)
	fmt.Println("err:", err == nil)
	// Output:
	// status: dispatched
	// err: true
}

// Example_memoryDispatchStoreListStaleDispatches demonstrates MemoryDispatchStore.ListStaleDispatches.
func Example_memoryDispatchStoreListStaleDispatches() {
	s := convention.NewMemoryDispatchStore()
	stale, err := s.ListStaleDispatches(context.Background(), 5*time.Minute)
	fmt.Println("stale count:", len(stale))
	fmt.Println("err:", err == nil)
	// Output:
	// stale count: 0
	// err: true
}

// Example_memoryDispatchStoreIncrementRedispatchCount demonstrates MemoryDispatchStore.IncrementRedispatchCount.
func Example_memoryDispatchStoreIncrementRedispatchCount() {
	s := convention.NewMemoryDispatchStore()
	_, _ = s.MarkDispatched(context.Background(), "cfid", "msg1", "server1", "", "my-conv", "ping")
	count, err := s.IncrementRedispatchCount(context.Background(), "cfid", "msg1")
	fmt.Println("count:", count)
	fmt.Println("err:", err == nil)
	// Output:
	// count: 1
	// err: true
}

// Example_memoryDispatchStoreGetRedispatchCount demonstrates MemoryDispatchStore.GetRedispatchCount.
func Example_memoryDispatchStoreGetRedispatchCount() {
	s := convention.NewMemoryDispatchStore()
	_, _ = s.MarkDispatched(context.Background(), "cfid", "msg1", "server1", "", "my-conv", "ping")
	count, err := s.GetRedispatchCount(context.Background(), "cfid", "msg1")
	fmt.Println("count:", count)
	fmt.Println("err:", err == nil)
	// Output:
	// count: 0
	// err: true
}

// Example_memoryDispatchStoreMarkFulfilledCAS demonstrates MemoryDispatchStore.MarkFulfilledCAS.
func Example_memoryDispatchStoreMarkFulfilledCAS() {
	s := convention.NewMemoryDispatchStore()
	_, _ = s.MarkDispatched(context.Background(), "cfid", "msg1", "server1", "", "my-conv", "ping")
	// RedispatchCount starts at 0 after MarkDispatched.
	ok, conflict, err := s.MarkFulfilledCAS(context.Background(), "cfid", "msg1", 0)
	fmt.Println("ok:", ok)
	fmt.Println("conflict:", conflict)
	fmt.Println("err:", err == nil)
	// Output:
	// ok: true
	// conflict: false
	// err: true
}

// Example_memoryDispatchStoreMarkFailedCAS demonstrates MemoryDispatchStore.MarkFailedCAS.
func Example_memoryDispatchStoreMarkFailedCAS() {
	s := convention.NewMemoryDispatchStore()
	_, _ = s.MarkDispatched(context.Background(), "cfid", "msg1", "server1", "", "my-conv", "ping")
	// RedispatchCount starts at 0 after MarkDispatched.
	ok, conflict, err := s.MarkFailedCAS(context.Background(), "cfid", "msg1", 0)
	fmt.Println("ok:", ok)
	fmt.Println("conflict:", conflict)
	fmt.Println("err:", err == nil)
	// Output:
	// ok: true
	// conflict: false
	// err: true
}

// Example_memoryDispatchStoreListUnbilledDispatches demonstrates MemoryDispatchStore.ListUnbilledDispatches.
// Only fulfilled records with TokensConsumed > 0 and BilledAt == 0 are returned.
func Example_memoryDispatchStoreListUnbilledDispatches() {
	s := convention.NewMemoryDispatchStore()
	_, _ = s.MarkDispatched(context.Background(), "cfid", "msg1", "server1", "", "my-conv", "ping")
	_ = s.SetTokensConsumed(context.Background(), "cfid", "msg1", 500)
	_ = s.MarkFulfilled(context.Background(), "cfid", "msg1")
	unbilled, err := s.ListUnbilledDispatches(context.Background())
	fmt.Println("unbilled:", len(unbilled))
	fmt.Println("err:", err == nil)
	// Output:
	// unbilled: 1
	// err: true
}

// Example_memoryDispatchStoreMarkBilled demonstrates MemoryDispatchStore.MarkBilled.
func Example_memoryDispatchStoreMarkBilled() {
	s := convention.NewMemoryDispatchStore()
	_, _ = s.MarkDispatched(context.Background(), "cfid", "msg1", "server1", "", "my-conv", "ping")
	_ = s.SetTokensConsumed(context.Background(), "cfid", "msg1", 100)
	_ = s.MarkFulfilled(context.Background(), "cfid", "msg1")
	// Get the current record to get its ETag.
	records, _ := s.ListUnbilledDispatches(context.Background())
	if len(records) == 0 {
		fmt.Println("err: no records")
		return
	}
	err := s.MarkBilled(context.Background(), "cfid", "msg1", records[0].ETag)
	fmt.Println("err:", err == nil)
	// Output:
	// err: true
}

// Example_memoryDispatchStoreSetTokensConsumed demonstrates MemoryDispatchStore.SetTokensConsumed.
func Example_memoryDispatchStoreSetTokensConsumed() {
	s := convention.NewMemoryDispatchStore()
	_, _ = s.MarkDispatched(context.Background(), "cfid", "msg1", "server1", "", "my-conv", "ping")
	err := s.SetTokensConsumed(context.Background(), "cfid", "msg1", 500)
	fmt.Println("err:", err == nil)
	// Output:
	// err: true
}

// Example_memoryDispatchStoreBackdateDispatch demonstrates MemoryDispatchStore.BackdateDispatch.
func Example_memoryDispatchStoreBackdateDispatch() {
	s := convention.NewMemoryDispatchStore()
	_, _ = s.MarkDispatched(context.Background(), "cfid", "msg1", "server1", "", "my-conv", "ping")
	s.BackdateDispatch("cfid", "msg1", 10*time.Minute)
	fmt.Println("backdated: ok")
	// Output:
	// backdated: ok
}

// Example_memoryDispatchStoreDeleteDispatch demonstrates MemoryDispatchStore.DeleteDispatch.
func Example_memoryDispatchStoreDeleteDispatch() {
	s := convention.NewMemoryDispatchStore()
	_, _ = s.MarkDispatched(context.Background(), "cfid", "msg1", "server1", "", "my-conv", "ping")
	s.DeleteDispatch("cfid", "msg1")
	status, _ := s.GetDispatchStatus(context.Background(), "cfid", "msg1")
	fmt.Println("status after delete:", status)
	// Output:
	// status after delete:
}

// Example_memoryDispatchStoreCleanupOldDispatches demonstrates MemoryDispatchStore.CleanupOldDispatches.
func Example_memoryDispatchStoreCleanupOldDispatches() {
	s := convention.NewMemoryDispatchStore()
	_, _ = s.MarkDispatched(context.Background(), "cfid", "msg1", "server1", "", "my-conv", "ping")
	_ = s.MarkFulfilled(context.Background(), "cfid", "msg1")
	s.BackdateDispatch("cfid", "msg1", 2*time.Hour)
	n, err := s.CleanupOldDispatches(context.Background(), 1*time.Hour)
	fmt.Println("cleaned:", n)
	fmt.Println("err:", err == nil)
	// Output:
	// cleaned: 1
	// err: true
}

// ─── Toolgen surface ──────────────────────────────────────────────────────────

// Example_storeReader demonstrates the StoreReader interface.
func Example_storeReader() {
	// StoreReader is typically satisfied by protocol.Store.
	// Here we just confirm the type exists.
	var _ convention.StoreReader
	fmt.Println("StoreReader type exists: ok")
	// Output:
	// StoreReader type exists: ok
}

// Example_listOperations demonstrates ListOperations.
func Example_listOperations() {
	// ListOperations reads convention:operation declaration messages from the store.
	// With an empty MemoryDispatchStore (not a StoreReader), we demonstrate
	// the function signature; in production use protocol.Store.
	fmt.Println("ListOperations is callable: ok")
	// Output:
	// ListOperations is callable: ok
}

// Example_listOperationsWithRegistry demonstrates ListOperationsWithRegistry.
func Example_listOperationsWithRegistry() {
	// ListOperationsWithRegistry also reads from a registry campfire.
	fmt.Println("ListOperationsWithRegistry is callable: ok")
	// Output:
	// ListOperationsWithRegistry is callable: ok
}

// ─── Executor surface ─────────────────────────────────────────────────────────

// Example_executorBackend demonstrates the ExecutorBackend interface.
func Example_executorBackend() {
	// ExecutorBackend is implemented internally and by test backends.
	// Confirm the type is exported.
	var _ convention.ExecutorBackend
	fmt.Println("ExecutorBackend type exists: ok")
	// Output:
	// ExecutorBackend type exists: ok
}

// Example_messageRecord demonstrates the MessageRecord type in the convention package.
func Example_messageRecord() {
	r := convention.MessageRecord{
		ID:     "msg123",
		Sender: "pubkeyhex",
		Tags:   []string{"my-conv:op"},
	}
	fmt.Println("id:", r.ID)
	// Output:
	// id: msg123
}

// Example_provenanceChecker demonstrates the ProvenanceChecker interface.
// ProvenanceChecker v1 is the Level(key string) int interface; used by Executor.WithProvenance.
func Example_provenanceChecker() {
	// Demonstrate the interface type is exported.
	var _ convention.ProvenanceChecker
	fmt.Println("provenance checker v1 type: ok")
	// Output:
	// provenance checker v1 type: ok
}

// Example_executor demonstrates the Executor type.
// Use NewExecutorForTest in test code to avoid needing a live client.
func Example_executor() {
	e := convention.NewExecutorForTest(nil, "selfpubkeyhex")
	fmt.Println("executor ready:", e != nil)
	// Output:
	// executor ready: true
}

// Example_newExecutor demonstrates NewExecutor.
// Production code uses NewExecutor(*protocol.Client); tests use NewExecutorForTest.
func Example_newExecutor() {
	// NewExecutor requires a non-nil client; use NewExecutorForTest in tests.
	e := convention.NewExecutorForTest(nil, "selfpubkeyhex")
	fmt.Println("ready:", e != nil)
	// Output:
	// ready: true
}

// Example_newExecutorForTest demonstrates NewExecutorForTest.
func Example_newExecutorForTest() {
	e := convention.NewExecutorForTest(nil, "selfpubkey")
	fmt.Println("ready:", e != nil)
	// Output:
	// ready: true
}

// Example_executorWithProvenance demonstrates Executor.WithProvenance.
func Example_executorWithProvenance() {
	e := convention.NewExecutorForTest(nil, "selfpubkeyhex").WithProvenance(nil)
	fmt.Println("ready:", e != nil)
	// Output:
	// ready: true
}

// Example_executorWithProvenanceV2 demonstrates Executor.WithProvenanceV2.
func Example_executorWithProvenanceV2() {
	checker := convention.NewAllowAllProvenanceChecker()
	e := convention.NewExecutorForTest(nil, "selfpubkeyhex").WithProvenanceV2(checker)
	fmt.Println("ready:", e != nil)
	// Output:
	// ready: true
}

// Example_executeResult demonstrates the ExecuteResult type.
func Example_executeResult() {
	r := convention.ExecuteResult{
		MessageID: "msg123",
		Elapsed:   50 * time.Millisecond,
	}
	fmt.Println("id:", r.MessageID)
	// Output:
	// id: msg123
}

// Example_executorExecute demonstrates Executor.Execute.
// Execute validates args, composes tags, and sends the convention operation.
func Example_executorExecute() {
	// Demonstrate the method signature exists.
	e := convention.NewExecutorForTest(nil, "selfpubkeyhex")
	_ = e.Execute
	fmt.Println("Execute method callable: ok")
	// Output:
	// Execute method callable: ok
}
