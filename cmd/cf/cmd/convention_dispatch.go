package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/spf13/pflag"
)

// cliStoreReader adapts store.Store to convention.StoreReader.
type cliStoreReader struct{ s store.Store }

func (r cliStoreReader) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	return r.s.ListMessages(campfireID, afterTimestamp, filter...)
}

// listConventionOperations returns the declarations for campfireID.
// In Trust v0.2, declarations are read directly from the campfire (inline).
// The chain-walker registry fallback is removed — conventions are adopted locally,
// not traced to an external root. Operators promote declarations into their campfires.
func listConventionOperations(ctx context.Context, s store.Store, campfireID string) ([]*convention.Declaration, error) {
	reader := cliStoreReader{s}
	decls, err := convention.ListOperations(ctx, reader, campfireID, "")
	if err != nil {
		return nil, fmt.Errorf("reading inline declarations: %w", err)
	}
	return decls, nil
}

// dispatchConventionOp dispatches a convention operation on a campfire.
// campfireName may be a name, alias, or ID. operationName is the operation to execute.
// rawArgs are the remaining CLI arguments (flags).
func dispatchConventionOp(ctx context.Context, campfireName string, operationName string, rawArgs []string) error {
	agentID, s, err := requireAgentAndStore()
	if err != nil {
		return err
	}
	defer s.Close()

	campfireID, err := resolveCampfireID(campfireName, s)
	if err != nil {
		return fmt.Errorf("resolving campfire %q: %w", campfireName, err)
	}

	// Read declarations from this campfire.
	decls, err := listConventionOperations(ctx, s, campfireID)
	if err != nil {
		return fmt.Errorf("reading declarations from campfire: %w", err)
	}

	// Default operation (no operation given) = delegate to read subcommand.
	// Do NOT call s.Close() here — the deferred close at the top of the function
	// handles it. A second close causes a panic.
	if operationName == "" {
		return readCmd.RunE(readCmd, []string{campfireID})
	}

	// Built-in: help lists available convention operations and views.
	if operationName == "help" {
		viewNames := listViewNames(s, campfireID)
		if len(decls) == 0 && len(viewNames) == 0 {
			fmt.Printf("Campfire %s — no convention operations or views declared.\n", campfireID[:shortIDLen])
			fmt.Println("\nUse cf send / cf read for raw messaging.")
			return nil
		}
		if len(decls) > 0 {
			fmt.Printf("Campfire %s — %d convention operations:\n\n", campfireID[:shortIDLen], len(decls))
			for _, d := range decls {
				desc := d.Description
				if len(desc) > 72 {
					desc = desc[:72] + "…"
				}
				fmt.Printf("  %-24s %s\n", d.Operation, desc)
			}
		}
		if len(viewNames) > 0 {
			fmt.Printf("\n%d named views (read operations):\n\n", len(viewNames))
			for _, name := range viewNames {
				fmt.Printf("  %-24s (view)\n", name)
			}
		}
		fmt.Printf("\nUsage: cf %s <operation> [--args]\n", campfireName)
		if len(decls) > 0 {
			fmt.Printf("  e.g. cf %s %s --help\n", campfireName, decls[0].Operation)
		}
		return nil
	}

	// Find matching declaration
	var matched *convention.Declaration
	for _, d := range decls {
		if d.Operation == operationName {
			matched = d
			break
		}
	}

	// If not a write operation, check if it's a named view.
	if matched == nil {
		viewDef, viewErr := findLatestView(s, campfireID, operationName)
		if viewErr == nil && viewDef != nil {
			return runViewRead(campfireID, operationName)
		}
	}

	if matched == nil {
		// Collect both operations and views for the error message.
		var ops []string
		for _, d := range decls {
			ops = append(ops, d.Operation)
		}
		// List views too.
		viewNames := listViewNames(s, campfireID)
		ops = append(ops, viewNames...)
		if len(ops) == 0 {
			return fmt.Errorf("unknown operation %q — no convention operations declared in campfire %s\nUse cf send / cf read for raw messaging.", operationName, campfireID[:shortIDLen])
		}
		return fmt.Errorf("unknown operation %q — available: %s\nRun: cf %s help", operationName, strings.Join(ops, ", "), campfireName)
	}

	// Build flag set and arg lookup from declaration args.
	flags := pflag.NewFlagSet("op", pflag.ContinueOnError)
	argByName := make(map[string]convention.ArgDescriptor, len(matched.Args))
	for _, arg := range matched.Args {
		argByName[arg.Name] = arg
		switch {
		case arg.Repeated || arg.Type == "tag_set":
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
		if err == pflag.ErrHelp {
			// Per-operation help: show description and args.
			desc := matched.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Printf("%s/%s — %s\n", matched.Convention, matched.Operation, desc)
			fmt.Printf("\nUsage: cf %s %s [--args]\n\n", campfireName, operationName)
			fmt.Println("Arguments:")
			flags.PrintDefaults()
			return nil
		}
		return fmt.Errorf("parsing flags: %w", err)
	}

	// Build args map from changed flags only, expanding enum short forms.
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
			val := f.Value.String()
			// Enum short-form expansion: if the arg is an enum and the value
			// doesn't directly match, check if exactly one declared value ends
			// with ":<val>". Expand to the full form so the executor and tag
			// compositor see the canonical tag-prefixed value.
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
								match = "" // ambiguous — don't expand
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

	client := protocol.New(s, agentID)
	executor := convention.NewExecutor(client, agentID.PublicKeyHex())

	if err := executor.Execute(ctx, matched, campfireID, args); err != nil {
		return fmt.Errorf("convention operation failed: %w", err)
	}

	fmt.Printf("ok — operation %q dispatched to campfire %s\n", operationName, campfireID[:shortIDLen])
	return nil
}
