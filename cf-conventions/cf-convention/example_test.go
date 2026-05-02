package convention_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// makeTestDecl creates a minimal valid Declaration for use in examples.
// It uses SimpleConvention to avoid boilerplate.
func makeTestDecl() *convention.Declaration {
	decl := convention.SimpleConvention("status", "1.0", "report", "Report agent status").
		RequiredArg("agent_id", "string", "Agent identifier").
		ProducesTag("status", "exactly_one").
		Build()
	return &decl
}

// Example_simpleConvention demonstrates building a convention Declaration
// using the fluent DeclarationBuilder API.
func Example_simpleConvention() {
	decl := convention.SimpleConvention("status", "1.0", "report", "Report agent status").
		RequiredArg("agent_id", "string", "Agent identifier").
		Arg("details", "string", "Optional details").
		ProducesTag("status", "exactly_one").
		Build()

	fmt.Println("convention:", decl.Convention)
	fmt.Println("operation:", decl.Operation)
	fmt.Println("args:", len(decl.Args))
	fmt.Println("tags:", len(decl.ProducesTags))
	// Output:
	// convention: status
	// operation: report
	// args: 2
	// tags: 1
}

// Example_declarationBuilder_arg demonstrates adding an optional argument.
func Example_declarationBuilder_arg() {
	decl := convention.SimpleConvention("myconv", "1.0", "ping", "Ping operation").
		Arg("message", "string", "Optional ping message").
		Build()

	fmt.Println("arg name:", decl.Args[0].Name)
	fmt.Println("arg required:", decl.Args[0].Required)
	// Output:
	// arg name: message
	// arg required: false
}

// Example_declarationBuilder_requiredArg demonstrates adding a required argument.
func Example_declarationBuilder_requiredArg() {
	decl := convention.SimpleConvention("myconv", "1.0", "ping", "Ping operation").
		RequiredArg("target", "string", "Required ping target").
		Build()

	fmt.Println("arg name:", decl.Args[0].Name)
	fmt.Println("arg required:", decl.Args[0].Required)
	// Output:
	// arg name: target
	// arg required: true
}

// Example_declarationBuilder_producesTag demonstrates adding a tag rule.
func Example_declarationBuilder_producesTag() {
	decl := convention.SimpleConvention("myconv", "1.0", "emit", "Emit an event").
		ProducesTag("myconv:event", "exactly_one").
		Build()

	fmt.Println("tag:", decl.ProducesTags[0].Tag)
	fmt.Println("cardinality:", decl.ProducesTags[0].Cardinality)
	// Output:
	// tag: myconv:event
	// cardinality: exactly_one
}

// Example_declarationBuilder_build demonstrates finalizing a Declaration.
func Example_declarationBuilder_build() {
	decl := convention.SimpleConvention("workflow", "2.0", "start", "Start a workflow").
		Build()

	fmt.Println("convention:", decl.Convention)
	fmt.Println("version:", decl.Version)
	fmt.Println("signing:", decl.Signing)
	// Output:
	// convention: workflow
	// version: 2.0
	// signing: member_key
}

// Example_parse demonstrates parsing and validating a convention declaration
// message from its JSON payload.
func Example_parse() {
	payload := []byte(`{
		"convention": "status",
		"version": "1.0",
		"operation": "report",
		"description": "Report agent status",
		"signing": "member_key",
		"args": [{"name": "agent_id", "type": "string", "required": true}],
		"produces_tags": [{"tag": "status", "cardinality": "exactly_one"}]
	}`)

	senderKey := "aabbcc"
	campfireKey := "ddeeff"
	tags := []string{"convention:operation"}

	decl, result, err := convention.Parse(tags, payload, senderKey, campfireKey, convention.DefaultDeniedTagPrefixes)
	if err != nil {
		panic(err)
	}

	fmt.Println("valid:", result.Valid)
	fmt.Println("operation:", decl.Operation)
	fmt.Println("args:", len(decl.Args))
	// Output:
	// valid: true
	// operation: report
	// args: 1
}

// Example_lint demonstrates running the linter on a convention declaration payload.
func Example_lint() {
	decl := makeTestDecl()
	payload, err := json.Marshal(decl)
	if err != nil {
		panic(err)
	}

	result := convention.Lint(payload)
	fmt.Println("valid:", result.Valid)
	fmt.Println("errors:", len(result.Errors))
	// Output:
	// valid: true
	// errors: 0
}

// Example_generateTool demonstrates generating an MCP tool descriptor
// from a parsed convention Declaration.
func Example_generateTool() {
	decl := makeTestDecl()
	tool, err := convention.GenerateTool(decl, "cfid")
	if err != nil {
		panic(err)
	}

	fmt.Println("tool name:", tool.Name)
	fmt.Println("description set:", tool.Description != "")
	// Output:
	// tool name: report
	// description set: true
}

// Example_generateToolName demonstrates picking a tool name, using a
// namespaced name when the primary name collides.
func Example_generateToolName() {
	decl := makeTestDecl()
	existing := map[string]bool{}

	name := convention.GenerateToolName(decl, existing)
	fmt.Println("primary name:", name)

	// Simulate collision.
	existing["report"] = true
	namespaced := convention.GenerateToolName(decl, existing)
	fmt.Println("namespaced name:", namespaced)
	// Output:
	// primary name: report
	// namespaced name: status_report
}

// Example_namespacedToolName demonstrates explicitly generating the
// convention-namespaced tool name.
func Example_namespacedToolName() {
	decl := makeTestDecl()
	name := convention.NamespacedToolName(decl)
	fmt.Println("name:", name)
	// Output:
	// name: status_report
}

// Example_newServer demonstrates constructing a convention Server and
// registering a handler.
func Example_newServer() {
	dir, err := os.MkdirTemp("", "campfire-example-server-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	client, _, err := protocol.Init(dir)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	decl := makeTestDecl()
	srv := convention.NewServer(client, decl)
	srv.RegisterHandler("report", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		return &convention.Response{Payload: map[string]any{"ok": true}}, nil
	})

	fmt.Println("server created:", srv != nil)
	// Output:
	// server created: true
}

// Example_server_withPollInterval demonstrates setting the poll interval on a Server.
func Example_server_withPollInterval() {
	dir, err := os.MkdirTemp("", "campfire-example-server-poll-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	client, _, err := protocol.Init(dir)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	decl := makeTestDecl()
	srv := convention.NewServer(client, decl).
		WithPollInterval(500)

	fmt.Println("server configured:", srv != nil)
	// Output:
	// server configured: true
}

// Example_server_withErrorHandler demonstrates attaching an error handler to a Server.
func Example_server_withErrorHandler() {
	dir, err := os.MkdirTemp("", "campfire-example-errhandler-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	client, _, err := protocol.Init(dir)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	decl := makeTestDecl()
	srv := convention.NewServer(client, decl).
		WithErrorHandler(func(err error) {
			// log the error
			_ = err
		})

	fmt.Println("server configured:", srv != nil)
	// Output:
	// server configured: true
}

// Example_server_withIdentityResolver demonstrates attaching an IdentityResolver.
func Example_server_withIdentityResolver() {
	dir, err := os.MkdirTemp("", "campfire-example-resolver-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	client, _, err := protocol.Init(dir)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	decl := makeTestDecl()
	srv := convention.NewServer(client, decl).
		WithIdentityResolver(convention.NoopIdentityResolver{})

	fmt.Println("server configured:", srv != nil)
	// Output:
	// server configured: true
}

// Example_server_registerHandler demonstrates registering a handler for an operation.
func Example_server_registerHandler() {
	dir, err := os.MkdirTemp("", "campfire-example-reghandler-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	client, _, err := protocol.Init(dir)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	decl := makeTestDecl()
	srv := convention.NewServer(client, decl)

	srv.RegisterHandler("report", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		return &convention.Response{Payload: map[string]any{"status": "ok"}}, nil
	})

	fmt.Println("handler registered:", true)
	// Output:
	// handler registered: true
}

// Example_newMemoryDispatchStore demonstrates creating an in-memory DispatchStore.
func Example_newMemoryDispatchStore() {
	store := convention.NewMemoryDispatchStore()
	fmt.Println("store created:", store != nil)
	// Output:
	// store created: true
}

// Example_newConventionDispatcher demonstrates creating a ConventionDispatcher.
func Example_newConventionDispatcher() {
	store := convention.NewMemoryDispatchStore()
	dispatcher := convention.NewConventionDispatcher(store, nil)
	fmt.Println("dispatcher created:", dispatcher != nil)
	// Output:
	// dispatcher created: true
}

// customEval is a minimal GateEvaluator used for example purposes only.
// It is not a stub (not AllowAllGateEvaluator), so GateEvaluatorSet() returns true.
type customEval struct{}

func (customEval) Evaluate(_ context.Context, _ convention.EvaluateRequest) convention.EvaluateResult {
	return convention.EvaluateResult{Decision: convention.GateAllow}
}

// Example_conventionDispatcher_gateEvaluatorSet demonstrates checking whether
// a GateEvaluator has been installed.
func Example_conventionDispatcher_gateEvaluatorSet() {
	store := convention.NewMemoryDispatchStore()
	dispatcher := convention.NewConventionDispatcher(store, nil)

	fmt.Println("gate set before:", dispatcher.GateEvaluatorSet())
	dispatcher.SetGateEvaluator(customEval{})
	fmt.Println("gate set after:", dispatcher.GateEvaluatorSet())
	// Output:
	// gate set before: false
	// gate set after: true
}

// Example_allowAllGateEvaluator demonstrates the AllowAllGateEvaluator stub
// that always returns GateAllow.
func Example_allowAllGateEvaluator() {
	eval := convention.AllowAllGateEvaluator{}
	result := eval.Evaluate(context.Background(), convention.EvaluateRequest{})
	fmt.Println("allowed:", result.Decision == convention.GateAllow)
	// Output:
	// allowed: true
}

// Example_newAllowAllProvenanceChecker demonstrates the stub ProvenanceCheckerV2
// that always returns ProvenanceLevelPresent.
func Example_newAllowAllProvenanceChecker() {
	checker := convention.NewAllowAllProvenanceChecker()
	req := convention.ProvenanceRequest{SenderKey: "aabbcc"}
	result := checker.CheckProvenance(context.Background(), req)
	fmt.Println("level:", result.Level == convention.ProvenanceLevelPresent)
	// Output:
	// level: true
}

// Example_noopIdentityResolver demonstrates the NoopIdentityResolver that
// always returns empty IdentityInfo.
func Example_noopIdentityResolver() {
	resolver := convention.NoopIdentityResolver{}
	fmt.Println("name:", resolver.Name())
	// Output:
	// name: noop
}

// Example_newSweeper demonstrates creating a Sweeper for orphaned dispatch recovery.
func Example_newSweeper() {
	store := convention.NewMemoryDispatchStore()
	dispatcher := convention.NewConventionDispatcher(store, nil)
	sw := convention.NewSweeper(dispatcher, store, nil)
	fmt.Println("sweeper created:", sw != nil)
	// Output:
	// sweeper created: true
}

// Example_request demonstrates the Request struct fields available to a handler.
func Example_request() {
	req := convention.Request{
		MessageID:  "msg123",
		Sender:     "aabbcc",
		CampfireID: "cfid",
		Args:       map[string]any{"agent_id": "my-agent"},
		Tags:       []string{"status"},
	}
	fmt.Println("message:", req.MessageID)
	fmt.Println("arg:", req.Args["agent_id"])
	// Output:
	// message: msg123
	// arg: my-agent
}

// Example_response demonstrates constructing a Response to return from a handler.
func Example_response() {
	resp := convention.Response{
		Payload:        map[string]any{"ok": true},
		Tags:           []string{"status"},
		TokensConsumed: 150,
	}
	fmt.Println("tokens:", resp.TokensConsumed)
	// Output:
	// tokens: 150
}

// Example_declaration demonstrates the core Declaration struct fields.
func Example_declaration() {
	decl := convention.Declaration{
		Convention: "status",
		Version:    "1.0",
		Operation:  "report",
		Signing:    "member_key",
	}
	fmt.Println("convention:", decl.Convention)
	fmt.Println("operation:", decl.Operation)
	// Output:
	// convention: status
	// operation: report
}

// Example_argDescriptor demonstrates the ArgDescriptor struct.
func Example_argDescriptor() {
	arg := convention.ArgDescriptor{
		Name:        "agent_id",
		Type:        "string",
		Required:    true,
		Description: "Agent identifier",
	}
	fmt.Println("name:", arg.Name)
	fmt.Println("required:", arg.Required)
	// Output:
	// name: agent_id
	// required: true
}

// Example_tagRule demonstrates the TagRule struct.
func Example_tagRule() {
	rule := convention.TagRule{
		Tag:         "status",
		Cardinality: "exactly_one",
	}
	fmt.Println("tag:", rule.Tag)
	fmt.Println("cardinality:", rule.Cardinality)
	// Output:
	// tag: status
	// cardinality: exactly_one
}

// Example_provenanceLevel demonstrates the ProvenanceLevel constants.
func Example_provenanceLevel() {
	fmt.Println("anonymous:", int(convention.ProvenanceLevelAnonymous))
	fmt.Println("claimed:", int(convention.ProvenanceLevelClaimed))
	fmt.Println("contactable:", int(convention.ProvenanceLevelContactable))
	fmt.Println("present:", int(convention.ProvenanceLevelPresent))
	// Output:
	// anonymous: 0
	// claimed: 1
	// contactable: 2
	// present: 3
}

// Example_provenanceRequest demonstrates the ProvenanceRequest struct.
func Example_provenanceRequest() {
	req := convention.ProvenanceRequest{SenderKey: "aabbcc"}
	fmt.Println("key:", req.SenderKey)
	// Output:
	// key: aabbcc
}

// Example_provenanceResult demonstrates the ProvenanceResult struct.
func Example_provenanceResult() {
	result := convention.ProvenanceResult{Level: convention.ProvenanceLevelPresent}
	fmt.Println("level:", int(result.Level))
	// Output:
	// level: 3
}

// Example_dispatchRecord demonstrates the DispatchRecord struct.
func Example_dispatchRecord() {
	rec := convention.DispatchRecord{
		CampfireID: "cfid",
		MessageID:  "msg123",
		Status:     "dispatched",
	}
	fmt.Println("campfire:", rec.CampfireID)
	fmt.Println("status:", rec.Status)
	// Output:
	// campfire: cfid
	// status: dispatched
}

// Example_lintFinding demonstrates the LintFinding struct.
func Example_lintFinding() {
	finding := convention.LintFinding{
		Severity: convention.LintError,
		Field:    "convention",
		Message:  "convention field is required",
	}
	fmt.Println("severity:", finding.Severity)
	fmt.Println("field:", finding.Field)
	// Output:
	// severity: error
	// field: convention
}

// Example_lintResult demonstrates the LintResult struct.
func Example_lintResult() {
	result := convention.LintResult{
		Valid:   true,
		Errors:  []convention.LintFinding{},
	}
	fmt.Println("valid:", result.Valid)
	fmt.Println("errors:", len(result.Errors))
	// Output:
	// valid: true
	// errors: 0
}

// Example_isErrorResponse demonstrates checking whether a message is a
// convention error response.
func Example_isErrorResponse() {
	msg := &protocol.Message{
		ID:   "msg123",
		Tags: []string{"fulfills", "convention:error"},
	}
	fmt.Println("is error:", convention.IsErrorResponse(msg))
	// Output:
	// is error: true
}

// Example_parseErrorResponse demonstrates extracting the error message from
// a convention error response payload.
func Example_parseErrorResponse() {
	payload, _ := json.Marshal(convention.ErrorResponse{Error: "something failed"})
	msg := &protocol.Message{
		Tags:    []string{"convention:error"},
		Payload: payload,
	}
	errMsg, err := convention.ParseErrorResponse(msg)
	if err != nil {
		panic(err)
	}
	fmt.Println("error:", errMsg)
	// Output:
	// error: something failed
}

// Example_conventionOperationTag demonstrates the ConventionOperationTag constant.
func Example_conventionOperationTag() {
	fmt.Println(convention.ConventionOperationTag)
	// Output:
	// convention:operation
}

// Example_conventionRevokeTag demonstrates the ConventionRevokeTag constant.
func Example_conventionRevokeTag() {
	fmt.Println(convention.ConventionRevokeTag)
	// Output:
	// convention:revoke
}

// Example_infrastructureConvention demonstrates the InfrastructureConvention constant.
func Example_infrastructureConvention() {
	fmt.Println(convention.InfrastructureConvention)
	// Output:
	// convention-extension
}

// Example_defaultDeniedTagPrefixes demonstrates the default denylist used by Parse.
func Example_defaultDeniedTagPrefixes() {
	fmt.Println("count:", len(convention.DefaultDeniedTagPrefixes))
	fmt.Println("first:", convention.DefaultDeniedTagPrefixes[0])
	// Output:
	// count: 2
	// first: naming:
}

// Example_evaluateRequest demonstrates the EvaluateRequest struct.
func Example_evaluateRequest() {
	req := convention.EvaluateRequest{
		// Request, ChainMessages, RootPrincipal, CurrentTime, RevocationView,
		// and OwnerPolicy are the structured inputs — zero values are valid for examples.
	}
	fmt.Println("chain messages:", len(req.ChainMessages))
	fmt.Println("revocation view:", len(req.RevocationView))
	// Output:
	// chain messages: 0
	// revocation view: 0
}

// Example_mCPToolInfo demonstrates the MCPToolInfo struct.
func Example_mCPToolInfo() {
	info := convention.MCPToolInfo{
		Name:        "report",
		Description: "Report agent status",
	}
	fmt.Println("name:", info.Name)
	fmt.Println("description:", info.Description)
	// Output:
	// name: report
	// description: Report agent status
}

// Example_signerType demonstrates the SignerType constants.
func Example_signerType() {
	fmt.Println("member_key:", convention.SignerMemberKey)
	fmt.Println("campfire_key:", convention.SignerCampfireKey)
	fmt.Println("registry:", convention.SignerConventionRegistry)
	// Output:
	// member_key: member_key
	// campfire_key: campfire_key
	// registry: convention_registry
}

// Example_maxRedispatches demonstrates the MaxRedispatches constant.
func Example_maxRedispatches() {
	fmt.Println("max redispatches:", convention.MaxRedispatches)
	// Output:
	// max redispatches: 3
}

// Example_conformanceResult demonstrates the ConformanceResult struct.
func Example_conformanceResult() {
	result := convention.ConformanceResult{
		Valid:                 true,
		Trusted:               false,
		CampfireKeyAuthorized: true,
		VersionSuperseded:     false,
	}
	fmt.Println("valid:", result.Valid)
	fmt.Println("authorized:", result.CampfireKeyAuthorized)
	// Output:
	// valid: true
	// authorized: true
}

// Example_identityInfo demonstrates the IdentityInfo struct.
func Example_identityInfo() {
	info := convention.IdentityInfo{
		IdentityVerified: false,
	}
	fmt.Println("verified:", info.IdentityVerified)
	// Output:
	// verified: false
}
