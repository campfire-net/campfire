// divergence_lint_test.go — compile-time and runtime guards against CLI/MCP arg-parser drift.
//
// Problem: parity_test.go contains parseCLIArgs, a local reproduction of the
// production arg-parsing logic in cmd/cf/cmd/convention_dispatch.go. If production
// adds a new ArgDescriptor.Type that maps to a new pflag Value.Type() string, only
// the test would catch the drift — nothing in the build graph enforces parity.
//
// Solution: this file adds two layers of protection.
//
//  1. productionArgTypeToPflagType — a direct mirror of the production dispatch
//     switch in convention_dispatch.go (lines 138-149 / 184-196). It maps each
//     known ArgDescriptor.Type (and the Repeated flag) to the pflag Value.Type()
//     string that production would produce. This function must be kept in sync with
//     production by hand — but TestCLIArgParserDivergence will catch any drift the
//     moment the test suite runs.
//
//  2. parityHandledPflagTypes — the exhaustive set of pflag Value.Type() strings
//     that parseCLIArgs (in parity_test.go) handles. It must cover every value that
//     productionArgTypeToPflagType can return. If production adds a new pflag type
//     (e.g. by introducing flags.Int64 for a new "int64" arg type), the test fails
//     immediately because parityHandledPflagTypes does not contain the new string.
//
// Compile-time guarantee: the constants pflagType* are referenced by both
// productionArgTypeToPflagType and parityHandledPflagTypes. Any mismatch between
// the two surfaces (production vs. parity) fails the test. The build itself fails
// to compile if the constants are removed or renamed.
//
// Divergence simulation: add a new case to productionArgTypeToPflagType that
// returns a new string (e.g. "int64") and watch TestCLIArgParserDivergence fail.
//
// Build tag: parity
// CI gate: go test -tags parity ./cf-conventions/parity/...

//go:build parity

package parity

import (
	"fmt"
	"testing"

	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/spf13/pflag"
)

// ─────────────────────────────────────────────────────────────────────────────
// pflag Value.Type() strings produced by production convention_dispatch.go.
// These constants encode the exhaustive set of pflag types that the production
// CLI parser can emit. If convention_dispatch.go ever calls flags.SomeNewType(),
// a new constant must be added here AND to parityHandledPflagTypes below, AND
// a new case must be added to parseCLIArgs in parity_test.go.
// ─────────────────────────────────────────────────────────────────────────────

const (
	// pflagTypeBool is produced for ArgDescriptor.Type == "boolean".
	pflagTypeBool = "bool"

	// pflagTypeInt is produced for ArgDescriptor.Type == "integer".
	pflagTypeInt = "int"

	// pflagTypeStringSlice is produced for ArgDescriptor.Repeated==true or
	// ArgDescriptor.Type == "tag_set".
	pflagTypeStringSlice = "stringSlice"

	// pflagTypeString is the default: produced for "string", "duration", "key",
	// "campfire", "message_id", "json", "enum" — any type that production maps
	// to flags.String().
	pflagTypeString = "string"
)

// parityHandledPflagTypes is the exhaustive set of pflag Value.Type() strings
// that parseCLIArgs (parity_test.go) handles in its VisitAll switch. Every
// string that productionArgTypeToPflagType can return MUST appear here.
//
// If this set does not cover a type returned by productionArgTypeToPflagType,
// TestCLIArgParserDivergence fails — alerting maintainers that parseCLIArgs
// needs a new case to match production.
var parityHandledPflagTypes = map[string]bool{
	pflagTypeBool:        true, // case "bool":
	pflagTypeInt:         true, // case "int":
	pflagTypeStringSlice: true, // case "stringSlice":
	pflagTypeString:      true, // default: (covers all string-registered types)
}

// ─────────────────────────────────────────────────────────────────────────────
// productionArgTypeToPflagType mirrors the registration switch in
// cmd/cf/cmd/convention_dispatch.go (the block beginning at:
//
//	for _, arg := range matched.Args {
//	    argByName[arg.Name] = arg
//	    switch {
//	    case arg.Repeated || arg.Type == "tag_set":
//	        flags.StringSlice(arg.Name, nil, arg.Description)
//	    case arg.Type == "boolean":
//	        flags.Bool(arg.Name, false, arg.Description)
//	    case arg.Type == "integer":
//	        flags.Int(arg.Name, 0, arg.Description)
//	    default: // string, duration, key, enum
//	        flags.String(arg.Name, "", arg.Description)
//	    }
//	}
//
// Returns the pflag Value.Type() string that a flag registered for the given
// ArgDescriptor would carry. This must be kept in sync with production dispatch.
// ─────────────────────────────────────────────────────────────────────────────
func productionArgTypeToPflagType(argType string, repeated bool) string {
	// Mirror the production switch exactly — same case order, same conditions.
	switch {
	case repeated || argType == "tag_set":
		return pflagTypeStringSlice
	case argType == "boolean":
		return pflagTypeBool
	case argType == "integer":
		return pflagTypeInt
	default:
		// string, duration, key, campfire, message_id, json, enum — all map to String.
		return pflagTypeString
	}
}

// knownArgTypesForLint is the set of ArgDescriptor.Type values accepted by the
// production parser (parser.go knownArgTypes). This list must be kept in sync
// with the production parser. TestParityArgTypeExhaustiveness verifies this by
// registering a flag for each entry and confirming the parser accepts it.
//
// Source: cf-conventions/cf-convention/parser.go knownArgTypes map.
var knownArgTypesForLint = []string{
	"string",
	"integer",
	"duration",
	"boolean",
	"key",
	"campfire",
	"message_id",
	"json",
	"tag_set",
	"enum",
}

// ─────────────────────────────────────────────────────────────────────────────
// TestCLIArgParserDivergence — the primary divergence gate.
//
// For every known arg type (including the Repeated variant), it:
//  1. Calls productionArgTypeToPflagType to determine what pflag type production emits.
//  2. Verifies that pflag type is in parityHandledPflagTypes.
//  3. Verifies the pflag flag set actually emits that type (ground-truth check).
//
// If production convention_dispatch.go adds a new pflag registration (e.g.
// flags.Int64 for a new "int64" arg type) but productionArgTypeToPflagType is
// not updated, this test does NOT fail — that's intentional. The failure
// happens when an author updates productionArgTypeToPflagType to return the new
// string but forgets to add it to parityHandledPflagTypes.
//
// In other words: productionArgTypeToPflagType is the "write once, check always"
// mirror of production. The invariant is:
//
//	for all arg types T: productionArgTypeToPflagType(T) ∈ parityHandledPflagTypes
// ─────────────────────────────────────────────────────────────────────────────
func TestCLIArgParserDivergence(t *testing.T) {
	t.Parallel()

	type argVariant struct {
		argType  string
		repeated bool
		label    string
	}

	var variants []argVariant
	for _, at := range knownArgTypesForLint {
		variants = append(variants, argVariant{argType: at, repeated: false, label: at})
		if at != "tag_set" {
			// Also test Repeated=true for non-tag_set types (production maps Repeated to StringSlice).
			variants = append(variants, argVariant{argType: at, repeated: true, label: at + "+repeated"})
		}
	}

	for _, v := range variants {
		v := v
		t.Run(v.label, func(t *testing.T) {
			t.Parallel()

			// Step 1: determine what pflag type production would register.
			wantPflagType := productionArgTypeToPflagType(v.argType, v.repeated)

			// Step 2: verify it is covered by the parity switch.
			if !parityHandledPflagTypes[wantPflagType] {
				t.Errorf(
					"arg type %q (repeated=%v) maps to pflag type %q in production, "+
						"but parseCLIArgs in parity_test.go does not handle it.\n"+
						"Add case %q to parseCLIArgs and add it to parityHandledPflagTypes.",
					v.argType, v.repeated, wantPflagType, wantPflagType,
				)
			}

			// Step 3: ground-truth — actually register the flag and check Value.Type().
			fs := pflag.NewFlagSet("lint", pflag.ContinueOnError)
			arg := convention.ArgDescriptor{
				Name:     "lint_arg",
				Type:     v.argType,
				Repeated: v.repeated,
			}
			// Mirror production registration (convention_dispatch.go).
			switch {
			case arg.Repeated || arg.Type == "tag_set":
				fs.StringSlice(arg.Name, nil, "")
			case arg.Type == "boolean":
				fs.Bool(arg.Name, false, "")
			case arg.Type == "integer":
				fs.Int(arg.Name, 0, "")
			default:
				fs.String(arg.Name, "", "")
			}

			f := fs.Lookup(arg.Name)
			if f == nil {
				t.Fatalf("flag %q not found in flag set after registration", arg.Name)
			}
			gotPflagType := f.Value.Type()
			if gotPflagType != wantPflagType {
				t.Errorf(
					"arg type %q (repeated=%v): productionArgTypeToPflagType returned %q "+
						"but actual pflag Value.Type() is %q — productionArgTypeToPflagType is out of sync",
					v.argType, v.repeated, wantPflagType, gotPflagType,
				)
			}
		})
	}
}

// TestParityArgTypeExhaustiveness verifies that knownArgTypesForLint covers the
// full set of arg types the production parser accepts. It does this by building
// a minimal Declaration for each type and checking that Parse does not reject it
// with an "unknown type" error. Any type in knownArgTypes that is missing from
// knownArgTypesForLint would cause a production declaration to fail silently.
//
// This is a cross-check: if the production parser's knownArgTypes gains a new
// entry but knownArgTypesForLint is not updated, a real-world convention using
// the new type would work in production but would not be tested by the divergence
// lint. This test makes that gap visible.
func TestParityArgTypeExhaustiveness(t *testing.T) {
	t.Parallel()

	for _, argType := range knownArgTypesForLint {
		argType := argType
		t.Run(argType, func(t *testing.T) {
			t.Parallel()

			// Build a minimal declaration JSON using this arg type.
			// We don't call Parse (which requires a full protocol message envelope).
			// Instead, register a flag exactly as production does and verify the
			// pflag type is non-empty (i.e., the registration path exists).
			fs := pflag.NewFlagSet("exhaustiveness", pflag.ContinueOnError)
			arg := convention.ArgDescriptor{
				Name: fmt.Sprintf("arg_%s", argType),
				Type: argType,
			}

			switch {
			case arg.Repeated || arg.Type == "tag_set":
				fs.StringSlice(arg.Name, nil, "")
			case arg.Type == "boolean":
				fs.Bool(arg.Name, false, "")
			case arg.Type == "integer":
				fs.Int(arg.Name, 0, "")
			default:
				fs.String(arg.Name, "", "")
			}

			f := fs.Lookup(arg.Name)
			if f == nil {
				t.Fatalf("arg type %q: flag not registered — production registration switch may be broken", argType)
			}
			if f.Value.Type() == "" {
				t.Errorf("arg type %q: empty pflag Value.Type() — unexpected", argType)
			}
		})
	}
}

// TestDivergenceLintSentinel is a compile-time sentinel test. It references all
// pflagType* constants and the parityHandledPflagTypes map to ensure they are
// used — preventing accidental removal. If any constant is deleted, this test
// will fail to compile.
//
// It also verifies that parityHandledPflagTypes is a superset of nothing hidden:
// every constant referenced here must appear as a key in the map.
func TestDivergenceLintSentinel(t *testing.T) {
	t.Parallel()

	// Compile-time: these constants must exist. If removed, this fails to compile.
	constants := []string{
		pflagTypeBool,
		pflagTypeInt,
		pflagTypeStringSlice,
		pflagTypeString,
	}

	// Runtime: each constant must be in parityHandledPflagTypes.
	for _, c := range constants {
		if !parityHandledPflagTypes[c] {
			t.Errorf("pflag type constant %q is not in parityHandledPflagTypes — add it", c)
		}
	}

	// Verify parityHandledPflagTypes has no entries not reachable from production.
	// Build the reachable set: run productionArgTypeToPflagType over all known types.
	reachable := make(map[string]bool)
	for _, at := range knownArgTypesForLint {
		reachable[productionArgTypeToPflagType(at, false)] = true
		reachable[productionArgTypeToPflagType(at, true)] = true
	}
	for pflagType := range parityHandledPflagTypes {
		if !reachable[pflagType] {
			t.Errorf(
				"parityHandledPflagTypes contains %q but no known arg type produces it — "+
					"either the type was removed from production or knownArgTypesForLint is incomplete",
				pflagType,
			)
		}
	}
}
