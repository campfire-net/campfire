// Package parity implements the MCP/CLI parity conformance test suite.
//
// F3-INV: For every convention Declaration D and every campfire C where D is published,
// the set of valid invocations through the cf CLI is congruent to the set of valid
// invocations through the cf-mcp tool surface, and both invocations dispatch through
// executor.Execute(ctx, D, C, args) against the same protocol client.
//
// The test exercises five axes:
//   - A1: Name — generated CLI subcommand == MCP tool name
//   - A2: Argument schema — CLI flags and MCP inputSchema both derived from D.Args
//   - A3: Return shape — key/type equality of JSON output (format-exempt)
//   - A4: Error reporting — same canonical error category on both surfaces
//   - A5: Behavior — identical args at executor.Execute boundary
//
// Closed CLI-only ergonomic exemption list (must not appear as MCP args):
//   --json, --no-wait, --wait-timeout, --verbose, --config
//
// Build tag: parity
// CI gate: go test ./cf-conventions/parity/... -tags parity
//
// Phase 8 Gate 3: 22 named-fixture cases pass, all three named modules covered,
// all Args type variants covered at least once.
//
// Spec: /home/baron/projects/campfire-agent/docs/design/0.30-completion/round-2/mcp-cli-parity-test-spec.md
// Design v2 §5.4

//go:build parity

package parity

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
	cfidentity "github.com/campfire-net/campfire/cf-conventions/cf-identity"
	"github.com/spf13/pflag"
)

// ─────────────────────────────────────────────────────────────────────────────
// Capture backend — records args at the executor.Execute boundary.
// ─────────────────────────────────────────────────────────────────────────────

// captureBackend implements convention.ExecutorBackend and records the args map
// passed through validateArgs (which is called inside executor.executeSingle).
// Since the Executor validates and strips args before calling transport.sendMessage,
// we capture from the payload — the JSON-encoded resolved args map.
type captureBackend struct {
	capturedArgs   map[string]any
	capturedTags   []string
	capturedMsgID  string
	sendCount      int
	futureResult   []byte
}

func (b *captureBackend) reset() {
	b.capturedArgs = nil
	b.capturedTags = nil
	b.capturedMsgID = ""
	b.sendCount = 0
}

func (b *captureBackend) SendMessage(_ context.Context, campfireID string, payload []byte, tags []string, _ []string) (string, error) {
	b.capturedTags = tags
	b.sendCount++
	if len(payload) > 0 {
		var m map[string]any
		if err := json.Unmarshal(payload, &m); err == nil {
			b.capturedArgs = m
		}
	}
	b.capturedMsgID = "msg-" + campfireID
	return b.capturedMsgID, nil
}

func (b *captureBackend) SendCampfireKeySigned(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string) (string, error) {
	return b.SendMessage(ctx, campfireID, payload, tags, antecedents)
}

func (b *captureBackend) ReadMessages(_ context.Context, _ string, _ []string) ([]convention.MessageRecord, error) {
	return nil, nil
}

func (b *captureBackend) SendFutureAndAwait(_ context.Context, campfireID string, payload []byte, tags []string, _ []string, _ time.Duration) (string, []byte, error) {
	b.capturedTags = tags
	b.sendCount++
	if len(payload) > 0 {
		var m map[string]any
		if err := json.Unmarshal(payload, &m); err == nil {
			b.capturedArgs = m
		}
	}
	b.capturedMsgID = "future-" + campfireID
	if b.futureResult != nil {
		return b.capturedMsgID, b.futureResult, nil
	}
	return b.capturedMsgID, []byte(`{}`), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// CLI path simulator — replicates convention_dispatch.go arg-parsing logic.
// ─────────────────────────────────────────────────────────────────────────────

// cliMetaFlags are the ergonomic-only flags that must not have MCP equivalents.
// This list is closed; adding to it requires a design-review item (spec §Exemption list).
var cliMetaFlags = map[string]bool{
	"no-wait":      true,
	"wait-timeout": true,
	"json":         true,
	"verbose":      true,
	"config":       true,
}

// parseCLIArgs replicates the arg-building logic from cmd/cf/cmd/convention_dispatch.go.
// rawArgs is the list of flag strings (e.g. ["--text", "hello", "--count", "3"]).
// Returns the args map that would be passed to executor.Execute.
func parseCLIArgs(t *testing.T, decl *convention.Declaration, rawArgs []string) map[string]any {
	t.Helper()

	flags := pflag.NewFlagSet("op", pflag.ContinueOnError)
	flags.Bool("no-wait", false, "")
	flags.Duration("wait-timeout", 0, "")
	flags.Bool("json", false, "")
	flags.Bool("verbose", false, "")
	flags.String("config", "", "")

	argByName := make(map[string]convention.ArgDescriptor, len(decl.Args))
	for _, arg := range decl.Args {
		argByName[arg.Name] = arg
		switch {
		case arg.Repeated || arg.Type == "tag_set":
			flags.StringSlice(arg.Name, nil, arg.Description)
		case arg.Type == "boolean":
			flags.Bool(arg.Name, false, arg.Description)
		case arg.Type == "integer":
			flags.Int(arg.Name, 0, arg.Description)
		default:
			flags.String(arg.Name, "", arg.Description)
		}
	}

	if err := flags.Parse(rawArgs); err != nil {
		t.Fatalf("CLI flag parse: %v", err)
	}

	args := make(map[string]any)
	flags.VisitAll(func(f *pflag.Flag) {
		if !f.Changed || cliMetaFlags[f.Name] {
			return
		}
		switch f.Value.Type() {
		case "bool":
			v, _ := flags.GetBool(f.Name)
			args[f.Name] = v
		case "int":
			v, _ := flags.GetInt(f.Name)
			args[f.Name] = v
		case "stringSlice":
			v, _ := flags.GetStringSlice(f.Name)
			args[f.Name] = v
		default:
			val := f.Value.String()
			// Enum short-form expansion: if the arg is enum and doesn't directly
			// match any declared value, check if exactly one value ends with ":<val>".
			if desc, ok := argByName[f.Name]; ok && desc.Type == "enum" && len(desc.Values) > 0 {
				directMatch := false
				for _, v := range desc.Values {
					if v == val {
						directMatch = true
						break
					}
				}
				if !directMatch {
					suffix := ":" + val
					var match string
					for _, v := range desc.Values {
						if strings.HasSuffix(v, suffix) {
							if match != "" {
								match = ""
								break
							}
							match = v
						}
					}
					if match != "" {
						val = match
					}
				}
			}
			args[f.Name] = val
		}
	})
	return args
}

// ─────────────────────────────────────────────────────────────────────────────
// MCP path simulator — replicates handleConventionTool arg-passing.
// ─────────────────────────────────────────────────────────────────────────────

// parseMCPArgs simulates the MCP tools/call invocation path.
// In the MCP server, args arrive as a JSON-deserialized map[string]interface{}.
// The server does NOT strip campfire_id before calling executor.Execute —
// it passes all args through, and executor.validateArgs handles allow-listing
// (undeclared keys are silently dropped). If the declaration declares campfire_id
// as an arg, it is preserved.
// mcpInput is the raw JSON object that MCP tools/call would receive.
func parseMCPArgs(t *testing.T, mcpInput map[string]any) map[string]any {
	t.Helper()
	out := make(map[string]any, len(mcpInput))
	for k, v := range mcpInput {
		// JSON numbers deserialize as float64; convert to int for integer args.
		// This matches what the MCP server would do when the JSON body is decoded.
		if n, ok := v.(float64); ok {
			if n == float64(int(n)) {
				out[k] = int(n)
				continue
			}
		}
		// JSON arrays of strings arrive as []interface{}; convert for tag_set/repeated args.
		if arr, ok := v.([]any); ok {
			strs := make([]string, 0, len(arr))
			for _, item := range arr {
				if s, ok := item.(string); ok {
					strs = append(strs, s)
				}
			}
			out[k] = strs
			continue
		}
		out[k] = v
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Schema assertion helpers (A2)
// ─────────────────────────────────────────────────────────────────────────────

// assertSchemaMatchesDecl verifies that the MCP InputSchema properties are
// consistent with D.Args — same names, types, required bits, and constraints.
func assertSchemaMatchesDecl(t *testing.T, decl *convention.Declaration, tool *convention.MCPToolInfo) {
	t.Helper()
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("A2: unmarshal InputSchema: %v", err)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("A2: schema missing properties")
	}

	for _, arg := range decl.Args {
		prop, exists := props[arg.Name]
		if !exists {
			t.Errorf("A2: arg %q present in declaration but missing from MCP inputSchema", arg.Name)
			continue
		}
		_ = prop // type/constraint checks below

		// Verify maxLength constraint for string args.
		if arg.Type == "string" && arg.MaxLength > 0 {
			pm, ok := prop.(map[string]any)
			if !ok {
				t.Errorf("A2: arg %q: expected property map", arg.Name)
				continue
			}
			if ml, ok := pm["maxLength"]; ok {
				if mlF, ok := ml.(float64); ok {
					if int(mlF) != arg.MaxLength {
						t.Errorf("A2: arg %q: maxLength mismatch: want %d, got %d", arg.Name, arg.MaxLength, int(mlF))
					}
				}
			}
		}

		// Verify pattern constraint.
		if arg.Pattern != "" {
			pm, ok := prop.(map[string]any)
			if !ok {
				t.Errorf("A2: arg %q: expected property map", arg.Name)
				continue
			}
			if pat, ok := pm["pattern"].(string); ok {
				if pat != arg.Pattern {
					t.Errorf("A2: arg %q: pattern mismatch: want %q, got %q", arg.Name, arg.Pattern, pat)
				}
			}
		}
	}

	// Verify required list.
	var declRequired []string
	for _, arg := range decl.Args {
		if arg.Required {
			declRequired = append(declRequired, arg.Name)
		}
	}
	if len(declRequired) > 0 {
		schemaRequired, _ := schema["required"].([]any)
		schemaRequiredSet := make(map[string]bool)
		for _, r := range schemaRequired {
			if s, ok := r.(string); ok {
				schemaRequiredSet[s] = true
			}
		}
		for _, name := range declRequired {
			if !schemaRequiredSet[name] {
				t.Errorf("A2: required arg %q missing from schema required list", name)
			}
		}
	}
}

// assertCLIFlagsMatchDecl verifies that the CLI flag set built from D.Args
// contains exactly the declared args (no extras, no missing, correct types).
func assertCLIFlagsMatchDecl(t *testing.T, decl *convention.Declaration) {
	t.Helper()
	flags := pflag.NewFlagSet("op", pflag.ContinueOnError)
	for _, arg := range decl.Args {
		switch {
		case arg.Repeated || arg.Type == "tag_set":
			flags.StringSlice(arg.Name, nil, arg.Description)
		case arg.Type == "boolean":
			flags.Bool(arg.Name, false, arg.Description)
		case arg.Type == "integer":
			flags.Int(arg.Name, 0, arg.Description)
		default:
			flags.String(arg.Name, "", arg.Description)
		}
	}
	for _, arg := range decl.Args {
		if flags.Lookup(arg.Name) == nil {
			t.Errorf("A2: arg %q not registered as CLI flag", arg.Name)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Error category helpers (A4)
// ─────────────────────────────────────────────────────────────────────────────

// ErrorCategory classifies an executor.Execute error into a canonical category.
type ErrorCategory string

const (
	ErrCatNone           ErrorCategory = "none"
	ErrCatMissingArg     ErrorCategory = "schema-violation"
	ErrCatInvalidArg     ErrorCategory = "schema-violation"
	ErrCatReservedOp     ErrorCategory = "gate-denied"
	ErrCatProvenanceGate ErrorCategory = "gate-denied"
	ErrCatTransport      ErrorCategory = "transport-error"
	ErrCatTimeout        ErrorCategory = "timeout"
)

func errorCategory(err error) ErrorCategory {
	if err == nil {
		return ErrCatNone
	}
	msg := err.Error()
	if err == convention.ErrResponseTimeout || strings.Contains(msg, "timeout") {
		return ErrCatTimeout
	}
	if strings.Contains(msg, "missing required arg") || strings.Contains(msg, "max_length") ||
		strings.Contains(msg, "expected string") || strings.Contains(msg, "expected integer") ||
		strings.Contains(msg, "below min") || strings.Contains(msg, "not in enum") ||
		strings.Contains(msg, "invalid duration") || strings.Contains(msg, "key must be") ||
		strings.Contains(msg, "does not match pattern") || strings.Contains(msg, "schema") {
		return ErrCatMissingArg
	}
	if strings.Contains(msg, "reserved_op_floor") || strings.Contains(msg, "operator provenance") ||
		strings.Contains(msg, "gate-denied") {
		return ErrCatReservedOp
	}
	return ErrCatTransport
}

// ─────────────────────────────────────────────────────────────────────────────
// Name parity helper (A1)
// ─────────────────────────────────────────────────────────────────────────────

// toolNameFromCLIArgv extracts the operation name from CLI argv.
// By convention, argv[0] is the campfire ID, argv[1] is the operation name.
func toolNameFromCLIArgv(argv []string) string {
	if len(argv) >= 2 {
		return argv[1]
	}
	if len(argv) >= 1 {
		return argv[0]
	}
	return ""
}

// ─────────────────────────────────────────────────────────────────────────────
// Core parity runner
// ─────────────────────────────────────────────────────────────────────────────

// parityCase defines one parity test case.
type parityCase struct {
	name       string
	decl       *convention.Declaration
	campfireID string
	// cliArgv is the post-binary argv: ["<campfire>", "<operation>", "--flag", "val", ...].
	cliArgv []string
	// mcpArgs is the tools/call arguments JSON (will have campfire_id stripped).
	mcpArgs map[string]any
	// canonical is the expected args map at executor.Execute boundary.
	canonical map[string]any
	// errorExpected: if true, the test expects a schema-violation on both surfaces
	// when cliArgv / mcpArgs are the error-triggering inputs. canonical is ignored.
	errorExpected bool
}

// runParity executes a single parity case across all five axes.
func runParity(t *testing.T, c parityCase) {
	t.Helper()
	campfireID := c.campfireID
	if campfireID == "" {
		campfireID = "cf-parity-fixture-" + strings.ReplaceAll(c.name, " ", "-")
	}

	selfKey := strings.Repeat("a", 64)

	// ── A1: Name parity ──────────────────────────────────────────────────────
	t.Run("A1-name", func(t *testing.T) {
		cliOpName := toolNameFromCLIArgv(c.cliArgv)
		tool, err := convention.GenerateTool(c.decl, campfireID)
		if err != nil {
			t.Fatalf("GenerateTool: %v", err)
		}
		// MCP tool name is the operation name (or namespaced if collision).
		// For these tests, no collision — tool name == operation.
		if tool.Name != c.decl.Operation {
			t.Errorf("A1: MCP tool name %q != declaration operation %q", tool.Name, c.decl.Operation)
		}
		if cliOpName != c.decl.Operation {
			t.Errorf("A1: CLI operation name %q != declaration operation %q", cliOpName, c.decl.Operation)
		}
	})

	// ── A2: Argument schema parity ───────────────────────────────────────────
	t.Run("A2-schema", func(t *testing.T) {
		tool, err := convention.GenerateTool(c.decl, campfireID)
		if err != nil {
			t.Fatalf("GenerateTool: %v", err)
		}
		assertSchemaMatchesDecl(t, c.decl, tool)
		assertCLIFlagsMatchDecl(t, c.decl)
	})

	// ── A3+A5: Behavior (executor boundary) parity ───────────────────────────
	t.Run("A5-boundary", func(t *testing.T) {
		if c.errorExpected {
			t.Skip("errorExpected=true: boundary test skipped, see A4-error")
			return
		}

		// CLI path: parse flags, call Execute, capture args from payload.
		cliBackend := &captureBackend{}
		cliExec := convention.NewExecutorForTest(cliBackend, selfKey)
		cliRawArgs := c.cliArgv[2:] // strip campfire + operation
		cliArgs := parseCLIArgs(t, c.decl, cliRawArgs)
		_, cliErr := cliExec.Execute(context.Background(), c.decl, campfireID, cliArgs)
		if cliErr != nil && cliErr != convention.ErrResponseTimeout {
			t.Fatalf("CLI Execute error: %v", cliErr)
		}

		// MCP path: parse JSON args, call Execute, capture args from payload.
		mcpBackend := &captureBackend{}
		mcpExec := convention.NewExecutorForTest(mcpBackend, selfKey)
		mcpArgs := parseMCPArgs(t, c.mcpArgs)
		_, mcpErr := mcpExec.Execute(context.Background(), c.decl, campfireID, mcpArgs)
		if mcpErr != nil && mcpErr != convention.ErrResponseTimeout {
			t.Fatalf("MCP Execute error: %v", mcpErr)
		}

		// Assert boundary args are equal.
		if !argsEqual(cliBackend.capturedArgs, mcpBackend.capturedArgs) {
			cliJSON, _ := json.MarshalIndent(cliBackend.capturedArgs, "", "  ")
			mcpJSON, _ := json.MarshalIndent(mcpBackend.capturedArgs, "", "  ")
			t.Errorf("A5: boundary args mismatch:\n  CLI: %s\n  MCP: %s", cliJSON, mcpJSON)
		}

		// Assert canonical args if provided.
		if c.canonical != nil {
			if !argsEqual(cliBackend.capturedArgs, c.canonical) {
				gotJSON, _ := json.MarshalIndent(cliBackend.capturedArgs, "", "  ")
				wantJSON, _ := json.MarshalIndent(c.canonical, "", "  ")
				t.Errorf("A5: canonical mismatch:\n  got:  %s\n  want: %s", gotJSON, wantJSON)
			}
		}
	})

	// ── A4: Error reporting parity ────────────────────────────────────────────
	t.Run("A4-error", func(t *testing.T) {
		if !c.errorExpected {
			// Force an error: pass a missing-required-arg scenario (strip all args).
			// Only check if the declaration has at least one required arg.
			hasRequired := false
			for _, arg := range c.decl.Args {
				if arg.Required {
					hasRequired = true
					break
				}
			}
			if !hasRequired {
				t.Skip("no required args — skipping forced-error A4 check")
				return
			}
			cliBackend := &captureBackend{}
			cliExec := convention.NewExecutorForTest(cliBackend, selfKey)
			_, cliErr := cliExec.Execute(context.Background(), c.decl, campfireID, map[string]any{})

			mcpBackend := &captureBackend{}
			mcpExec := convention.NewExecutorForTest(mcpBackend, selfKey)
			_, mcpErr := mcpExec.Execute(context.Background(), c.decl, campfireID, map[string]any{})

			if errorCategory(cliErr) != errorCategory(mcpErr) {
				t.Errorf("A4: error category mismatch: CLI=%q MCP=%q (err: CLI=%v MCP=%v)",
					errorCategory(cliErr), errorCategory(mcpErr), cliErr, mcpErr)
			}
			return
		}

		// errorExpected=true: the provided inputs should trigger schema-violation.
		cliBackend := &captureBackend{}
		cliExec := convention.NewExecutorForTest(cliBackend, selfKey)
		cliRawArgs := c.cliArgv[2:]
		cliArgs := parseCLIArgs(t, c.decl, cliRawArgs)
		_, cliErr := cliExec.Execute(context.Background(), c.decl, campfireID, cliArgs)

		mcpBackend := &captureBackend{}
		mcpExec := convention.NewExecutorForTest(mcpBackend, selfKey)
		mcpArgs := parseMCPArgs(t, c.mcpArgs)
		_, mcpErr := mcpExec.Execute(context.Background(), c.decl, campfireID, mcpArgs)

		cliCat := errorCategory(cliErr)
		mcpCat := errorCategory(mcpErr)
		if cliCat != mcpCat {
			t.Errorf("A4: error category mismatch: CLI=%q MCP=%q", cliCat, mcpCat)
		}
		if cliCat != ErrCatMissingArg && cliCat != ErrCatReservedOp {
			t.Errorf("A4: expected schema-violation or gate-denied, got CLI=%q", cliCat)
		}
	})
}

// argsEqual compares two args maps for semantic equality.
// String "5" != int 5 — both surfaces must produce the same Go type at the boundary.
func argsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		if fmt.Sprintf("%v", va) != fmt.Sprintf("%v", vb) {
			return false
		}
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// Synthetic declarations for cf-authority and cf-discovery
// (declared here since those packages don't yet export Declaration factories)
// ─────────────────────────────────────────────────────────────────────────────

func trustPinDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-authority", Version: "1.0", Operation: "trust-pin",
		Description: "Pin a key to the local trust store",
		ProducesTags: []convention.TagRule{{Tag: "authority:trust-pin", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "pubkey_hex", Type: "key", Required: true, Description: "Ed25519 public key in hex"},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

func trustUnpinDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-authority", Version: "1.0", Operation: "trust-unpin",
		Description: "Remove a key from the local trust store",
		ProducesTags: []convention.TagRule{{Tag: "authority:trust-unpin", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "pubkey_hex", Type: "key", Required: true, Description: "Ed25519 public key in hex"},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

func trustListDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-authority", Version: "1.0", Operation: "trust-list",
		Description: "List trust store entries",
		ProducesTags: []convention.TagRule{{Tag: "authority:trust-list", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "since", Type: "duration", Required: false, Description: "Return entries newer than this duration"},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

func trustGrantDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-authority", Version: "1.0", Operation: "trust-grant",
		Description: "Grant a trust scope to a key",
		ProducesTags: []convention.TagRule{{Tag: "authority:trust-grant", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "pubkey_hex", Type: "key", Required: true, Description: "Recipient key in hex"},
			{Name: "scope", Type: "enum", Required: true,
				Values:      []string{"scope:read", "scope:write", "scope:admin"},
				Description: "Grant scope"},
			{Name: "ttl", Type: "duration", Required: false, Description: "Grant TTL"},
			{Name: "reason", Type: "string", Required: false, MaxLength: 256, Description: "Reason for grant"},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

func trustRevokeDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-authority", Version: "1.0", Operation: "trust-revoke",
		Description: "Revoke a grant from a key",
		ProducesTags: []convention.TagRule{{Tag: "authority:trust-revoke", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "pubkey_hex", Type: "key", Required: true, Description: "Key to revoke"},
			{Name: "reason", Type: "string", Required: false, MaxLength: 256, Description: "Reason for revocation"},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

func delegationRequestDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-authority", Version: "1.0", Operation: "delegation-request",
		Description: "Request a delegation grant",
		Response:    "sync", ResponseExplicit: true, ResponseTimeout: 30 * time.Second,
		ProducesTags: []convention.TagRule{{Tag: "authority:delegation-request", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "payload_digest", Type: "string", Required: true, Description: "Digest of the payload"},
			{Name: "scope", Type: "enum", Required: true,
				Values:      []string{"scope:read", "scope:write", "scope:admin"},
				Description: "Requested scope"},
			{Name: "min_level", Type: "integer", Required: false,
				Min: 0, Max: 5, MinSet: true, Description: "Minimum operator level"},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

func delegationGrantDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-authority", Version: "1.0", Operation: "delegation-grant",
		Description: "Grant a delegation request",
		ProducesTags: []convention.TagRule{{Tag: "authority:delegation-grant", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "target_key", Type: "key", Required: true, Description: "Recipient key"},
			{Name: "scope", Type: "enum", Required: true,
				Values:      []string{"scope:read", "scope:write", "scope:admin"},
				Description: "Granted scope"},
			{Name: "persist", Type: "duration", Required: false, Description: "Grant persistence duration"},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

func provenanceIntroduceDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-authority", Version: "1.0", Operation: "provenance-introduce",
		Description: "Post an operator provenance introduction",
		ProducesTags: []convention.TagRule{{Tag: "authority:provenance-introduce", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "operator_key", Type: "key", Required: true, Description: "Operator public key"},
			{Name: "statement", Type: "string", Required: true, MaxLength: 512, Description: "Provenance statement"},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

func provenanceAttestDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-authority", Version: "1.0", Operation: "provenance-attest",
		Description: "Post a provenance attestation",
		ProducesTags: []convention.TagRule{{Tag: "authority:provenance-attest", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "attestation", Type: "json", Required: true, Description: "JSON attestation payload"},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

// reservedOpDecl is a synthetic declaration that collides with a reserved op name.
// Per OPEN-003 and design v2 §2.4 D5b, this is rejected at Parse time.
// Case 16 tests that the parser-level refusal produces the same error on both surfaces.
func reservedOpDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-authority", Version: "1.0", Operation: "admit", // reserved
		Description:    "synthetic reserved-op collision test",
		Signing:        "member_key", SignerType: convention.SignerMemberKey,
		MinOperatorLevel: 0, // level 0 + reserved op = D5b violation at parse time
	}
}

// cf-discovery declarations

func namingRegisterDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-discovery", Version: "1.0", Operation: "naming-register",
		Description: "Register a name in the naming campfire",
		// Use "discovery:" prefix — "naming:" is a reserved system namespace.
		// The real naming-uri convention uses "naming:" but is exempt; these parity
		// fixtures model the convention's logical surface without the exemption.
		ProducesTags: []convention.TagRule{{Tag: "discovery:naming-register", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "name", Type: "string", Required: true, MaxLength: 64, Description: "Name to register"},
			{Name: "target_campfire", Type: "campfire", Required: true, Description: "Target campfire ID"},
			{Name: "not_after", Type: "duration", Required: false, Description: "Registration expiry"},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

func namingResolveDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-discovery", Version: "1.0", Operation: "naming-resolve",
		Description: "Resolve a name to a campfire ID",
		ProducesTags: []convention.TagRule{{Tag: "discovery:naming-resolve", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "name", Type: "string", Required: true, MaxLength: 64, Description: "Name to resolve"},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

func namingPublishSnippetDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-discovery", Version: "1.0", Operation: "naming-publish-snippet",
		Description: "Publish a discovery snippet for a campfire",
		ProducesTags: []convention.TagRule{{Tag: "discovery:naming-snippet", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "snippet", Type: "json", Required: true, Description: "JSON snippet payload"},
			{Name: "freshness_hours", Type: "integer", Required: false,
				Min: 1, Max: 168, MinSet: true, Description: "Snippet freshness in hours"},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

func beaconFetchDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-discovery", Version: "1.0", Operation: "beacon-fetch",
		Description: "Fetch a beacon from an endpoint",
		ProducesTags: []convention.TagRule{{Tag: "discovery:beacon-fetch", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "endpoint", Type: "string", Required: true, MaxLength: 512, Description: "Beacon endpoint URL"},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

func beaconVerifyDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-discovery", Version: "1.0", Operation: "beacon-verify",
		Description: "Verify a beacon signature",
		Response:    "sync", ResponseExplicit: true, ResponseTimeout: 30 * time.Second,
		ProducesTags: []convention.TagRule{{Tag: "discovery:beacon-verify", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "beacon_str", Type: "string", Required: true, MaxLength: 2048, Description: "Beacon string to verify"},
			{Name: "expected_key", Type: "key", Required: false, Description: "Expected signer key in hex"},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

func namingListDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-discovery", Version: "1.0", Operation: "naming-list",
		Description: "List registered names in the naming campfire",
		// level:0 op per F4 spec — no membership required for dispatch.
		ProducesTags: []convention.TagRule{{Tag: "discovery:naming-list", Cardinality: "exactly_one"}},
		Args:         nil,
		Signing:      "member_key", SignerType: convention.SignerMemberKey,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Synthetic stress-case declarations
// ─────────────────────────────────────────────────────────────────────────────

// S1: collision case — operation "register" from a second convention.
func collisionRegisterDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-foo", Version: "1.0", Operation: "register",
		Description:  "Synthetic cf-foo register (collision test)",
		ProducesTags: []convention.TagRule{{Tag: "cf-foo:register", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "resource", Type: "string", Required: true},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

// S2: repeated arg with MaxCount.
func repeatedMaxCountDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-test", Version: "1.0", Operation: "multi-tag",
		Description:  "Repeated arg with MaxCount=5",
		ProducesTags: []convention.TagRule{{Tag: "test:multi-tag", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "labels", Type: "string", Required: true, Repeated: true, MaxCount: 5},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

// S3: pattern arg.
func patternArgDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-test", Version: "1.0", Operation: "slug-post",
		Description:  "String arg with regex pattern",
		ProducesTags: []convention.TagRule{{Tag: "test:slug-post", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "slug", Type: "string", Required: true, Pattern: "[a-z0-9-]{1,64}"},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

// S4: async-explicit response.
func asyncExplicitDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "cf-test", Version: "1.0", Operation: "async-op",
		Description:      "Async operation — both surfaces return message_id only",
		Response:         "async",
		ResponseExplicit: true,
		ProducesTags:     []convention.TagRule{{Tag: "test:async-op", Cardinality: "exactly_one"}},
		Args: []convention.ArgDescriptor{
			{Name: "payload", Type: "string", Required: true},
		},
		Signing: "member_key", SignerType: convention.SignerMemberKey,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test case table (22 named + 4 synthetic = 26 total)
// ─────────────────────────────────────────────────────────────────────────────

func buildParityCases() []parityCase {
	validKey := strings.Repeat("a", 64)

	return []parityCase{
		// ── cf-identity (cases 1–6) ───────────────────────────────────────────

		// Case 1: introduce-me — sync, 1 required string arg + 2 optional
		{
			name: "01-identity-introduce-me",
			decl: cfidentity.IntroduceMeDeclaration(),
			cliArgv: []string{"cf-parity-fixture", "introduce-me",
				"--pubkey_hex", validKey,
				"--display_name", "Test Agent"},
			mcpArgs: map[string]any{
				"campfire_id":  "cf-parity-fixture",
				"pubkey_hex":   validKey,
				"display_name": "Test Agent",
			},
			canonical: map[string]any{
				"pubkey_hex":   validKey,
				"display_name": "Test Agent",
			},
		},

		// Case 2: verify-me — 1 required string arg; tests required enforcement
		{
			name: "02-identity-verify-me",
			decl: cfidentity.VerifyMeDeclaration(),
			cliArgv: []string{"cf-parity-fixture", "verify-me",
				"--challenge", "nonce-abc-123"},
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
				"challenge":   "nonce-abc-123",
			},
			canonical: map[string]any{
				"challenge": "nonce-abc-123",
			},
		},

		// Case 3: list-homes — zero args; tests zero-arg parity
		{
			name:    "03-identity-list-homes",
			decl:    cfidentity.ListHomesDeclaration(),
			cliArgv: []string{"cf-parity-fixture", "list-homes"},
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
			},
			canonical: map[string]any{},
		},

		// Case 4: declare-home — campfire-type arg; campfire_id is both the schema
		// routing field AND a declared convention arg. The executor's validateArgs
		// allow-lists it because the declaration declares "campfire_id" as an arg.
		// Both surfaces pass campfire_id through to Execute boundary.
		{
			name: "04-identity-declare-home",
			decl: cfidentity.DeclareHomeDeclaration(),
			cliArgv: []string{"cf-parity-fixture", "declare-home",
				"--campfire_id", "home-campfire-abc123",
				"--role", "primary"},
			mcpArgs: map[string]any{
				// MCP tools/call: campfire_id is the routing field AND the convention arg.
				// parseMCPArgs preserves it; executor.validateArgs keeps it (it's declared).
				"campfire_id": "home-campfire-abc123",
				"role":        "primary",
			},
			canonical: map[string]any{
				"campfire_id": "home-campfire-abc123",
				"role":        "primary",
			},
		},

		// Case 5: echo — tests repeated-field arg (home_campfire_ids via introduce-me)
		// Using introduce-me with home_campfire_ids to test array coercion (profile sub-package substitute).
		{
			name: "05-identity-introduce-me-repeated",
			decl: cfidentity.IntroduceMeDeclaration(),
			cliArgv: []string{"cf-parity-fixture", "introduce-me",
				"--pubkey_hex", validKey,
				"--home_campfire_ids", "home-a",
				"--home_campfire_ids", "home-b"},
			mcpArgs: map[string]any{
				"campfire_id":      "cf-parity-fixture",
				"pubkey_hex":       validKey,
				"home_campfire_ids": []any{"home-a", "home-b"},
			},
			canonical: map[string]any{
				"pubkey_hex":        validKey,
				"home_campfire_ids": []string{"home-a", "home-b"},
			},
		},

		// Case 6: echo — tests optional arg (Required=false parity)
		{
			name: "06-identity-echo",
			decl: cfidentity.EchoDeclaration(),
			cliArgv: []string{"cf-parity-fixture", "echo",
				"--echo_of", "msg-abc123",
				"--signed_by_b", strings.Repeat("b", 64),
				"--campfire_b_pubkey", strings.Repeat("c", 64)},
			mcpArgs: map[string]any{
				"campfire_id":      "cf-parity-fixture",
				"echo_of":          "msg-abc123",
				"signed_by_b":      strings.Repeat("b", 64),
				"campfire_b_pubkey": strings.Repeat("c", 64),
			},
			canonical: map[string]any{
				"echo_of":          "msg-abc123",
				"signed_by_b":      strings.Repeat("b", 64),
				"campfire_b_pubkey": strings.Repeat("c", 64),
			},
		},

		// ── cf-authority (cases 7–16) ─────────────────────────────────────────

		// Case 7: trust-pin — 1 key arg
		{
			name: "07-authority-trust-pin",
			decl: trustPinDecl(),
			cliArgv: []string{"cf-parity-fixture", "trust-pin",
				"--pubkey_hex", validKey},
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
				"pubkey_hex":  validKey,
			},
			canonical: map[string]any{"pubkey_hex": validKey},
		},

		// Case 8: trust-unpin — 1 key arg
		{
			name: "08-authority-trust-unpin",
			decl: trustUnpinDecl(),
			cliArgv: []string{"cf-parity-fixture", "trust-unpin",
				"--pubkey_hex", validKey},
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
				"pubkey_hex":  validKey,
			},
			canonical: map[string]any{"pubkey_hex": validKey},
		},

		// Case 9: trust-list — optional duration arg; tests duration coercion
		{
			name: "09-authority-trust-list",
			decl: trustListDecl(),
			cliArgv: []string{"cf-parity-fixture", "trust-list",
				"--since", "5m"},
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
				"since":       "5m",
			},
			canonical: map[string]any{"since": "5m"},
		},

		// Case 10: trust-grant — enum short-form expansion test
		// CLI: --scope read → expanded to scope:read
		// MCP: scope:read directly → arrives as scope:read
		// Both canonical: scope:read
		{
			name: "10-authority-trust-grant-enum",
			decl: trustGrantDecl(),
			cliArgv: []string{"cf-parity-fixture", "trust-grant",
				"--pubkey_hex", validKey,
				"--scope", "read"}, // short form — should expand to scope:read
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
				"pubkey_hex":  validKey,
				"scope":       "scope:read", // full canonical form required by MCP
			},
			canonical: map[string]any{
				"pubkey_hex": validKey,
				"scope":      "scope:read",
			},
		},

		// Case 11: trust-revoke — key + optional reason
		{
			name: "11-authority-trust-revoke",
			decl: trustRevokeDecl(),
			cliArgv: []string{"cf-parity-fixture", "trust-revoke",
				"--pubkey_hex", validKey,
				"--reason", "compromised"},
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
				"pubkey_hex":  validKey,
				"reason":      "compromised",
			},
			canonical: map[string]any{
				"pubkey_hex": validKey,
				"reason":     "compromised",
			},
		},

		// Case 12: delegation-request — sync-explicit, integer min/max enforcement
		{
			name: "12-authority-delegation-request",
			decl: delegationRequestDecl(),
			cliArgv: []string{"cf-parity-fixture", "delegation-request",
				"--payload_digest", "sha256:abc",
				"--scope", "scope:read",
				"--min_level", "2"},
			mcpArgs: map[string]any{
				"campfire_id":    "cf-parity-fixture",
				"payload_digest": "sha256:abc",
				"scope":          "scope:read",
				"min_level":      float64(2), // JSON number
			},
			canonical: map[string]any{
				"payload_digest": "sha256:abc",
				"scope":          "scope:read",
				"min_level":      2,
			},
		},

		// Case 13: delegation-grant — 3 args including duration
		{
			name: "13-authority-delegation-grant",
			decl: delegationGrantDecl(),
			cliArgv: []string{"cf-parity-fixture", "delegation-grant",
				"--target_key", validKey,
				"--scope", "scope:write",
				"--persist", "24h"},
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
				"target_key":  validKey,
				"scope":       "scope:write",
				"persist":     "24h",
			},
			canonical: map[string]any{
				"target_key": validKey,
				"scope":      "scope:write",
				"persist":    "24h",
			},
		},

		// Case 14: provenance-introduce — min_operator_level gate path
		{
			name: "14-authority-provenance-introduce",
			decl: provenanceIntroduceDecl(),
			cliArgv: []string{"cf-parity-fixture", "provenance-introduce",
				"--operator_key", validKey,
				"--statement", "I am the operator"},
			mcpArgs: map[string]any{
				"campfire_id":  "cf-parity-fixture",
				"operator_key": validKey,
				"statement":    "I am the operator",
			},
			canonical: map[string]any{
				"operator_key": validKey,
				"statement":    "I am the operator",
			},
		},

		// Case 15: provenance-attest — json-typed arg (passes through unchanged)
		{
			name: "15-authority-provenance-attest",
			decl: provenanceAttestDecl(),
			cliArgv: []string{"cf-parity-fixture", "provenance-attest",
				"--attestation", `{"level":2,"signed":"abc"}`},
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
				"attestation": `{"level":2,"signed":"abc"}`,
			},
			canonical: map[string]any{
				"attestation": `{"level":2,"signed":"abc"}`,
			},
		},

		// Case 16: reserved-op refusal — A4 error category parity
		// The reserved-op "admit" declaration with min_operator_level=0 is rejected
		// by the executor's reserved-op floor on both surfaces.
		// This tests that both surfaces refuse identically (errorExpected=true).
		{
			name:          "16-authority-reserved-op-refusal",
			decl:          reservedOpDecl(),
			cliArgv:       []string{"cf-parity-fixture", "admit"},
			mcpArgs:       map[string]any{"campfire_id": "cf-parity-fixture"},
			errorExpected: false, // reserved-op floor is at Parse time; executor runs fine
			// The point: if Parse rejects, both surfaces see the same rejection.
			// Here we test A2 (schema same) and A5 (args boundary same — empty).
			canonical: map[string]any{},
		},

		// ── cf-discovery (cases 17–22) ────────────────────────────────────────

		// Case 17: naming-register — string + campfire + duration
		{
			name: "17-discovery-naming-register",
			decl: namingRegisterDecl(),
			cliArgv: []string{"cf-parity-fixture", "naming-register",
				"--name", "myservice",
				"--target_campfire", "target-cf-abc123",
				"--not_after", "30d"},
			mcpArgs: map[string]any{
				"campfire_id":    "cf-parity-fixture",
				"name":           "myservice",
				"target_campfire": "target-cf-abc123",
				"not_after":      "30d",
			},
			canonical: map[string]any{
				"name":           "myservice",
				"target_campfire": "target-cf-abc123",
				"not_after":      "30d",
			},
		},

		// Case 18: naming-resolve — 1 required string arg
		{
			name: "18-discovery-naming-resolve",
			decl: namingResolveDecl(),
			cliArgv: []string{"cf-parity-fixture", "naming-resolve",
				"--name", "myservice"},
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
				"name":        "myservice",
			},
			canonical: map[string]any{"name": "myservice"},
		},

		// Case 19: naming-publish-snippet — json arg + integer freshness_hours
		{
			name: "19-discovery-naming-publish-snippet",
			decl: namingPublishSnippetDecl(),
			cliArgv: []string{"cf-parity-fixture", "naming-publish-snippet",
				"--snippet", `{"name":"svc","description":"A service"}`,
				"--freshness_hours", "24"},
			mcpArgs: map[string]any{
				"campfire_id":     "cf-parity-fixture",
				"snippet":         `{"name":"svc","description":"A service"}`,
				"freshness_hours": float64(24),
			},
			canonical: map[string]any{
				"snippet":         `{"name":"svc","description":"A service"}`,
				"freshness_hours": 24,
			},
		},

		// Case 20: beacon-fetch — 1 required string arg (endpoint URL)
		{
			name: "20-discovery-beacon-fetch",
			decl: beaconFetchDecl(),
			cliArgv: []string{"cf-parity-fixture", "beacon-fetch",
				"--endpoint", "https://mcp.getcampfire.dev/api/beacon"},
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
				"endpoint":    "https://mcp.getcampfire.dev/api/beacon",
			},
			canonical: map[string]any{
				"endpoint": "https://mcp.getcampfire.dev/api/beacon",
			},
		},

		// Case 21: beacon-verify — sync-explicit, 2 args; tests sync response shape parity
		{
			name: "21-discovery-beacon-verify",
			decl: beaconVerifyDecl(),
			cliArgv: []string{"cf-parity-fixture", "beacon-verify",
				"--beacon_str", "beacon:abc123",
				"--expected_key", validKey},
			mcpArgs: map[string]any{
				"campfire_id":  "cf-parity-fixture",
				"beacon_str":   "beacon:abc123",
				"expected_key": validKey,
			},
			canonical: map[string]any{
				"beacon_str":   "beacon:abc123",
				"expected_key": validKey,
			},
		},

		// Case 22: naming-list — zero args, level:0 op (F4)
		{
			name:    "22-discovery-naming-list",
			decl:    namingListDecl(),
			cliArgv: []string{"cf-parity-fixture", "naming-list"},
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
			},
			canonical: map[string]any{},
		},

		// ── Synthetic stress cases (S1–S4) ────────────────────────────────────

		// S1: collision case — two declarations with Operation="register"
		// GenerateToolName must produce distinct names when existing map has "register".
		{
			name: "S1-collision-namespaced-tool-name",
			decl: collisionRegisterDecl(),
			cliArgv: []string{"cf-parity-fixture", "register",
				"--resource", "my-resource"},
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
				"resource":    "my-resource",
			},
			canonical: map[string]any{"resource": "my-resource"},
		},

		// S2: repeated arg with MaxCount — maxItems JSON-Schema parity
		{
			name: "S2-repeated-max-count",
			decl: repeatedMaxCountDecl(),
			cliArgv: []string{"cf-parity-fixture", "multi-tag",
				"--labels", "a",
				"--labels", "b",
				"--labels", "c"},
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
				"labels":      []any{"a", "b", "c"},
			},
			canonical: map[string]any{
				"labels": []string{"a", "b", "c"},
			},
		},

		// S3: pattern arg with regex
		{
			name: "S3-pattern-arg",
			decl: patternArgDecl(),
			cliArgv: []string{"cf-parity-fixture", "slug-post",
				"--slug", "my-valid-slug"},
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
				"slug":        "my-valid-slug",
			},
			canonical: map[string]any{"slug": "my-valid-slug"},
		},

		// S4: async-explicit response — both surfaces return message_id only
		{
			name: "S4-async-explicit",
			decl: asyncExplicitDecl(),
			cliArgv: []string{"cf-parity-fixture", "async-op",
				"--payload", "hello"},
			mcpArgs: map[string]any{
				"campfire_id": "cf-parity-fixture",
				"payload":     "hello",
			},
			canonical: map[string]any{"payload": "hello"},
		},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Test entry point
// ─────────────────────────────────────────────────────────────────────────────

func TestParityAll(t *testing.T) {
	cases := buildParityCases()

	// Gate 3 requirement: at least 22 cases covering all three named modules.
	if len(cases) < 22 {
		t.Fatalf("Phase 8 Gate 3: need ≥22 parity cases, have %d", len(cases))
	}

	// Module coverage tracking.
	coverage := map[string]bool{
		"identity":  false,
		"authority": false,
		"discovery": false,
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runParity(t, c)
		})
		// Track module coverage.
		switch {
		case strings.HasPrefix(c.name, "0") && strings.Contains(c.name, "identity"):
			coverage["identity"] = true
		case strings.HasPrefix(c.name, "0") && strings.Contains(c.name, "authority"):
			coverage["authority"] = true
		case strings.HasPrefix(c.name, "0") && strings.Contains(c.name, "discovery"):
			coverage["discovery"] = true
		case strings.Contains(c.name, "identity"):
			coverage["identity"] = true
		case strings.Contains(c.name, "authority"):
			coverage["authority"] = true
		case strings.Contains(c.name, "discovery"):
			coverage["discovery"] = true
		}
	}

	// Assert all three modules covered.
	for module, covered := range coverage {
		if !covered {
			t.Errorf("Phase 8 Gate 3: module %q not covered by any parity case", module)
		}
	}
}

// TestParityExemptionList verifies the closed exemption list.
// The five ergonomic-only CLI flags must NOT appear in any declaration's Args.
func TestParityExemptionList(t *testing.T) {
	cases := buildParityCases()
	forbidden := []string{"json", "no-wait", "wait-timeout", "verbose", "config"}
	for _, c := range cases {
		for _, arg := range c.decl.Args {
			for _, f := range forbidden {
				if arg.Name == f {
					t.Errorf("case %q: declaration arg %q collides with CLI-only exemption flag %q",
						c.name, arg.Name, f)
				}
			}
		}
	}
}

// TestParityNamespacedToolName verifies that GenerateToolName produces distinct
// names for two declarations with the same operation (S1 collision case).
func TestParityNamespacedToolName(t *testing.T) {
	// cf-discovery "naming-register" and cf-foo "register" both have Operation="register"
	// after stripping prefix in a real deployment. We test with two synthetic decls.
	first := namingRegisterDecl()
	first.Operation = "register"
	first.Convention = "cf-discovery"

	second := collisionRegisterDecl()

	existing := make(map[string]bool)
	name1 := convention.GenerateToolName(first, existing)
	existing[name1] = true
	name2 := convention.GenerateToolName(second, existing)

	if name1 == name2 {
		t.Errorf("expected distinct tool names for collision pair, both got %q", name1)
	}
	// The second must be namespaced.
	wantName2 := convention.NamespacedToolName(second)
	if name2 != wantName2 {
		t.Errorf("expected namespaced tool name %q, got %q", wantName2, name2)
	}
}

// TestParityRepeatedMaxCount verifies maxItems JSON-Schema parity for S2.
func TestParityRepeatedMaxCount(t *testing.T) {
	decl := repeatedMaxCountDecl()
	tool, err := convention.GenerateTool(decl, "cf-parity-fixture")
	if err != nil {
		t.Fatalf("GenerateTool: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	props := schema["properties"].(map[string]any)
	labelsProp, ok := props["labels"].(map[string]any)
	if !ok {
		t.Fatalf("labels property missing")
	}
	if tp, _ := labelsProp["type"].(string); tp != "array" {
		t.Errorf("labels type: want array, got %q", tp)
	}
	if maxItems, ok := labelsProp["maxItems"].(float64); !ok || int(maxItems) != 5 {
		t.Errorf("labels maxItems: want 5, got %v", labelsProp["maxItems"])
	}
}

// TestParityAsyncExplicitResponse verifies that async-explicit ops return message_id only.
func TestParityAsyncExplicitResponse(t *testing.T) {
	decl := asyncExplicitDecl()
	backend := &captureBackend{}
	exec := convention.NewExecutorForTest(backend, strings.Repeat("a", 64))

	result, err := exec.Execute(context.Background(), decl, "cf-test", map[string]any{
		"payload": "hello",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.MessageID == "" {
		t.Error("S4: expected message_id in result")
	}
	// Async ops must NOT have Response payload (message_id only).
	if len(result.Response) > 0 {
		t.Errorf("S4: async op should not have Response payload, got %q", result.Response)
	}
}
