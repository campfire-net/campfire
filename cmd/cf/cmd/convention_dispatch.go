package cmd

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/trust"
	"github.com/spf13/pflag"
)

// chainWalkerCache is a singleton registry of ChainWalkers keyed by rootKey.
// Walkers carry a 5-minute in-memory cache; reusing the same walker instance
// across listConventionOperations calls means the cache is actually hit.
// Without this, each call to listConventionOperations instantiates a fresh
// walker whose cache is always empty.
var (
	chainWalkersMu sync.Mutex
	chainWalkers   = make(map[string]*trust.ChainWalker)
)

// getOrCreateWalker returns the cached ChainWalker for rootKey, creating one
// if none exists yet. The supplied store and resolver are used only on first
// creation; subsequent calls return the existing walker (cache intact).
func getOrCreateWalker(rootKey string, cs trust.ChainStore, resolver trust.ChainResolver) *trust.ChainWalker {
	chainWalkersMu.Lock()
	defer chainWalkersMu.Unlock()
	if w, ok := chainWalkers[rootKey]; ok {
		return w
	}
	w := trust.NewChainWalker(rootKey, cs, resolver)
	chainWalkers[rootKey] = w
	return w
}

// resetChainWalkers clears the singleton registry. Used in tests to ensure
// isolation between test cases that use different stores or root keys.
func resetChainWalkers() {
	chainWalkersMu.Lock()
	chainWalkers = make(map[string]*trust.ChainWalker)
	chainWalkersMu.Unlock()
}

// cliStoreReader adapts store.Store to convention.StoreReader.
type cliStoreReader struct{ s store.Store }

func (r cliStoreReader) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	return r.s.ListMessages(campfireID, afterTimestamp, filter...)
}

// localChainResolver implements trust.ChainResolver for the CLI's local store.
// For locally-operated campfires, the root registry campfire ID equals the root key
// (campfire IDs are Ed25519 public keys encoded as hex, which is also the root key).
type localChainResolver struct{}

func (r localChainResolver) ResolveRootRegistry(_ context.Context, rootKey string) (string, error) {
	return rootKey, nil
}

// listConventionOperations returns the declarations for campfireID.
// It first checks for inline declarations (convention:operation messages in the
// campfire). If none are found, it walks the trust chain to find the convention
// registry campfire and reads declarations from there. Registry declarations
// can supersede inline declarations via the Supersedes field.
//
// The chain walk uses a 5-minute cache on the ChainWalker, so repeated calls
// within the same process do not re-walk the chain.
func listConventionOperations(ctx context.Context, s store.Store, campfireID string) ([]*convention.Declaration, error) {
	reader := cliStoreReader{s}

	// Check for inline declarations first.
	inlineDecls, err := convention.ListOperations(reader, campfireID, "")
	if err != nil {
		return nil, fmt.Errorf("reading inline declarations: %w", err)
	}
	if len(inlineDecls) > 0 {
		return inlineDecls, nil
	}

	// No inline declarations — try to find the convention registry via the trust chain.
	// The root key for a locally-operated campfire is the campfire ID itself.
	operatorRoot, err := naming.LoadOperatorRoot(CFHome())
	if err != nil || operatorRoot == nil {
		// No operator root configured — return empty list (offline fallback).
		return inlineDecls, nil
	}

	rootKey := operatorRoot.CampfireID
	walker := getOrCreateWalker(rootKey, cliChainStore{s}, localChainResolver{})
	chain, chainErr := walker.WalkChain(ctx)
	if chainErr != nil {
		// Chain walk failed — fall back to inline declarations (empty here).
		return inlineDecls, nil
	}

	if chain.ConventionRegID == "" {
		return inlineDecls, nil
	}

	// Read from registry, merging with any inline declarations (empty set here).
	// Registry declarations may supersede inline ones via the Supersedes field.
	return convention.ListOperationsWithRegistry(reader, campfireID, "", chain.ConventionRegID)
}

// cliChainStore adapts store.Store to trust.ChainStore.
type cliChainStore struct{ s store.Store }

func (c cliChainStore) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	return c.s.ListMessages(campfireID, afterTimestamp, filter...)
}

// dispatchConventionOp dispatches a convention operation on a campfire.
// campfireName may be a name, alias, or ID. operationName is the operation to execute.
// rawArgs are the remaining CLI arguments (flags).
func dispatchConventionOp(campfireName string, operationName string, rawArgs []string) error {
	agentID, s, err := requireAgentAndStore()
	if err != nil {
		return err
	}
	defer s.Close()

	campfireID, err := resolveCampfireID(campfireName, s)
	if err != nil {
		return fmt.Errorf("resolving campfire %q: %w", campfireName, err)
	}

	// Read declarations from this campfire, with registry fallback via trust chain.
	decls, err := listConventionOperations(context.Background(), s, campfireID)
	if err != nil {
		return fmt.Errorf("reading declarations from campfire: %w", err)
	}

	// Default operation (no operation given) = delegate to read subcommand.
	// Do NOT call s.Close() here — the deferred close at the top of the function
	// handles it. A second close causes a panic.
	if operationName == "" {
		return readCmd.RunE(readCmd, []string{campfireID})
	}

	// Find matching declaration
	var matched *convention.Declaration
	for _, d := range decls {
		if d.Operation == operationName {
			matched = d
			break
		}
	}
	if matched == nil {
		var ops []string
		for _, d := range decls {
			ops = append(ops, d.Operation)
		}
		if len(ops) == 0 {
			return fmt.Errorf("unknown operation %q — no convention operations declared in campfire %s", operationName, campfireID[:12])
		}
		return fmt.Errorf("unknown operation %q — available: %s", operationName, strings.Join(ops, ", "))
	}

	// Build flag set from declaration args
	flags := pflag.NewFlagSet("op", pflag.ContinueOnError)
	for _, arg := range matched.Args {
		switch {
		case arg.Repeated:
			flags.StringSlice(arg.Name, nil, arg.Description)
		case arg.Type == "boolean":
			flags.Bool(arg.Name, false, arg.Description)
		case arg.Type == "integer":
			flags.Int(arg.Name, 0, arg.Description)
		default: // string, duration, key, enum
			flags.String(arg.Name, "", arg.Description)
		}
	}

	if err := flags.Parse(rawArgs); err != nil {
		return fmt.Errorf("parsing flags: %w", err)
	}

	// Build args map from changed flags only
	args := make(map[string]any)
	flags.VisitAll(func(f *pflag.Flag) {
		if !f.Changed {
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
			args[f.Name] = f.Value.String()
		}
	})

	transport := &cliTransportAdapter{agentID: agentID, store: s}
	executor := convention.NewExecutor(transport, agentID.PublicKeyHex())

	if err := executor.Execute(context.Background(), matched, campfireID, args); err != nil {
		return fmt.Errorf("convention operation failed: %w", err)
	}

	fmt.Printf("ok — operation %q dispatched to campfire %s\n", operationName, campfireID[:12])
	return nil
}
