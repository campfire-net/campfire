package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/spf13/cobra"
)

// completeCampfireID provides tab completion for campfire ID arguments.
// It completes from:
//  1. Local memberships (campfire IDs the agent has joined)
//  2. Local beacons (discovered campfire beacons)
//  3. cf:// names (resolved from the network via naming protocol)
func completeCampfireID(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// If completing a cf:// URI, delegate to name completion
	if naming.IsCampfireURI(toComplete) || strings.HasPrefix(toComplete, "cf:") {
		return completeCampfireName(toComplete)
	}

	var completions []string

	// Complete from local memberships and beacons
	s, err := openStore()
	if err == nil {
		defer s.Close()
		memberships, err := s.ListMemberships()
		if err == nil {
			for _, m := range memberships {
				if strings.HasPrefix(m.CampfireID, toComplete) {
					// Show short prefix + description for readability
					desc := m.CampfireID[:12]
					if m.Description != "" {
						desc = m.Description
					}
					completions = append(completions, fmt.Sprintf("%s\t%s", m.CampfireID, desc))
				}
			}
		}
	}

	// Also suggest cf:// prefix if the user hasn't typed anything
	if toComplete == "" {
		completions = append(completions, "cf://\tResolve by name (cf://namespace.app.campfire)")
	}

	return completions, cobra.ShellCompDirectiveNoFileComp
}

// completeCampfireName handles tab completion for cf:// URIs.
// It resolves names from the network at each dot/slash boundary.
func completeCampfireName(toComplete string) ([]string, cobra.ShellCompDirective) {
	rootID := getRootRegistryID()
	if rootID == "" {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	// Parse what we have so far
	raw := toComplete
	if !strings.HasPrefix(strings.ToLower(raw), "cf://") {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	rest := raw[len("cf://"):]

	// Determine if we're completing after a dot (name segment) or slash (API endpoint)
	completingAPI := strings.Contains(rest, "/")

	if completingAPI {
		return completeAPIEndpoints(rootID, rest)
	}
	return completeNameSegments(rootID, rest)
}

// completeNameSegments completes dot-separated name segments.
// e.g., "aietf." → lists children of aietf campfire
func completeNameSegments(rootID, rest string) ([]string, cobra.ShellCompDirective) {
	parts := strings.Split(rest, ".")
	// The last part is what we're completing
	prefix := ""
	if len(parts) > 0 {
		prefix = parts[len(parts)-1]
	}

	// Resolve parent segments to find which campfire to query
	parentSegments := parts[:len(parts)-1]
	parentID := rootID

	if len(parentSegments) > 0 {
		resolver := newCLIResolver(rootID)
		if resolver == nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		ctx, cancel := context.WithTimeout(context.Background(), naming.DefaultCompletionTimeout)
		defer cancel()

		var err error
		parentID, err = resolver.ResolveName(ctx, parentSegments)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
	}

	// Query children of the parent campfire
	resolver := newCLIResolver(rootID)
	if resolver == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	ctx, cancel := context.WithTimeout(context.Background(), naming.DefaultCompletionTimeout)
	defer cancel()

	entries, err := resolver.ListChildren(ctx, parentID, prefix)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	// Build the completed prefix (everything before the last segment)
	completedPrefix := "cf://"
	if len(parentSegments) > 0 {
		completedPrefix += strings.Join(parentSegments, ".") + "."
	}

	var completions []string
	for _, e := range entries {
		desc := naming.SanitizeDescription(e.Description)
		completions = append(completions, fmt.Sprintf("%s%s\t%s", completedPrefix, e.Name, desc))
	}

	return completions, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
}

// completeAPIEndpoints completes slash-separated API endpoints.
// e.g., "aietf.social.lobby/" → lists declared futures in lobby
func completeAPIEndpoints(rootID, rest string) ([]string, cobra.ShellCompDirective) {
	slashIdx := strings.Index(rest, "/")
	namePart := rest[:slashIdx]
	pathPrefix := rest[slashIdx+1:]

	segments := strings.Split(namePart, ".")
	resolver := newCLIResolver(rootID)
	if resolver == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	ctx, cancel := context.WithTimeout(context.Background(), naming.DefaultCompletionTimeout)
	defer cancel()

	campfireID, err := resolver.ResolveName(ctx, segments)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	decls, err := resolver.ListAPI(ctx, campfireID)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	completedPrefix := "cf://" + namePart + "/"
	var completions []string
	for _, d := range decls {
		if strings.HasPrefix(d.Endpoint, pathPrefix) {
			desc := naming.SanitizeDescription(d.Description)
			completions = append(completions, fmt.Sprintf("%s%s\t%s", completedPrefix, d.Endpoint, desc))
		}
	}

	return completions, cobra.ShellCompDirectiveNoFileComp
}

// newCLIResolver creates a naming resolver using the local store.
// Returns nil if identity or store can't be loaded.
func newCLIResolver(rootID string) *naming.Resolver {
	agentID, s, err := requireAgentAndStore()
	if err != nil {
		return nil
	}
	// Note: store is not closed here — it must outlive the resolver.
	// This is acceptable for completion (short-lived process).
	_ = agentID

	transport := &naming.CLITransport{
		Identity: agentID,
		Store:    s,
	}
	return naming.NewResolver(transport, rootID)
}

// registerCompletions attaches campfire ID completion to all commands that
// take a campfire-id as their first positional argument.
func registerCompletions() {
	campfireIDCommands := []*cobra.Command{
		sendCmd, joinCmd, awaitCmd, readCmd,
		compactCmd, discoverCmd, memberCmd, membersCmd,
		admitCmd, evictCmd, leaveCmd, inspectCmd,
	}
	for _, c := range campfireIDCommands {
		c.ValidArgsFunction = completeCampfireID
	}
}

// ---------------------------------------------------------------------------
// Cisco-style "?" help — intercepted before Cobra dispatch
// ---------------------------------------------------------------------------

// interceptHelp checks if the args contain a "?" and handles it.
// Returns true if help was shown (caller should exit), false to continue normal dispatch.
//
// Patterns:
//
//	cf ?                         → list all commands
//	cf cf://aietf.social.?       → trailing ? stripped, show name children
//	cf cf://aietf.social. ?      → separate ?, previous arg is a URI prefix
//	cf cf://aietf.social.lobby/? → trailing ? on path, show API endpoints
//	cf send ?                    → show subcommand help
func interceptHelp(args []string) bool {
	if len(args) == 0 {
		return false
	}

	// Find ? in the arg list
	qIdx := -1
	for i, a := range args {
		if a == "?" {
			qIdx = i
			break
		}
		if strings.HasSuffix(a, "?") {
			// Trailing ? on an argument — strip it and treat as help on that prefix
			args[i] = strings.TrimSuffix(a, "?")
			qIdx = i
			break
		}
	}
	if qIdx < 0 {
		return false
	}

	// Collect everything before the ? position as context
	preceding := args[:qIdx]
	currentArg := args[qIdx] // the arg at ? position (with ? already stripped, or "?")

	// Case 1: "cf ?" — no preceding args, show all commands
	if len(preceding) == 0 && (currentArg == "?" || currentArg == "") {
		showCommandHelp()
		return true
	}

	// Case 2: "cf cf://aietf.social.?" — the current arg (with ? stripped) is a URI
	if currentArg != "" && currentArg != "?" {
		if naming.IsCampfireURI(currentArg) || strings.HasPrefix(strings.ToLower(currentArg), "cf:") {
			showNameHelp(currentArg)
			return true
		}
	}

	// Case 3: "cf cf://aietf.social. ?" — ? is separate, previous arg is a URI
	if len(preceding) > 0 {
		lastPreceding := preceding[len(preceding)-1]
		if naming.IsCampfireURI(lastPreceding) || strings.HasPrefix(strings.ToLower(lastPreceding), "cf:") {
			showNameHelp(lastPreceding)
			return true
		}

		// Case 4: "cf send ?" — preceding arg is a subcommand, show its help
		subcmd, _, err := rootCmd.Find(preceding)
		if err == nil && subcmd != rootCmd {
			subcmd.Help()
			return true
		}
	}

	// Fallback: show all commands
	showCommandHelp()
	return true
}

// showCommandHelp prints all available commands Cisco-style.
func showCommandHelp() error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Available commands:")
	fmt.Fprintln(w)

	for _, c := range rootCmd.Commands() {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		fmt.Fprintf(w, "  %s\t%s\n", c.Name(), c.Short)
	}
	w.Flush()

	fmt.Println()
	fmt.Println("Use 'cf <command> --help' for details on a specific command.")
	fmt.Println("Use 'cf cf://<name>.?' to explore the campfire name tree.")
	return nil
}

// showNameHelp resolves a partial cf:// URI and prints available completions.
func showNameHelp(input string) error {
	rootID := getRootRegistryID()
	if rootID == "" {
		fmt.Println("Root registry not configured. Set CF_ROOT_REGISTRY to enable name resolution.")
		return nil
	}

	// Strip trailing ? if present
	input = strings.TrimSuffix(input, "?")

	if !strings.HasPrefix(strings.ToLower(input), "cf://") {
		input = "cf://" + input
	}

	raw := input[len("cf://"):]

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	if strings.Contains(raw, "/") {
		// Show API endpoints
		slashIdx := strings.Index(raw, "/")
		namePart := raw[:slashIdx]

		segments := strings.Split(namePart, ".")
		resolver := newCLIResolver(rootID)
		if resolver == nil {
			fmt.Println("Cannot load identity or store.")
			return nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), naming.DefaultCompletionTimeout)
		defer cancel()

		campfireID, err := resolver.ResolveName(ctx, segments)
		if err != nil {
			fmt.Printf("Cannot resolve %s: %v\n", namePart, err)
			return nil
		}

		decls, err := resolver.ListAPI(ctx, campfireID)
		if err != nil {
			fmt.Printf("Cannot list API for %s: %v\n", namePart, err)
			return nil
		}

		fmt.Fprintf(w, "API endpoints for cf://%s:\n\n", namePart)
		if len(decls) == 0 {
			fmt.Fprintln(w, "  (no declared endpoints)")
		}
		for _, d := range decls {
			desc := naming.SanitizeDescription(d.Description)
			fmt.Fprintf(w, "  /%s\t%s\n", d.Endpoint, desc)
			for _, a := range d.Args {
				req := ""
				if a.Required {
					req = " (required)"
				}
				fmt.Fprintf(w, "    ?%s\t%s — %s%s\n", a.Name, a.Type, naming.SanitizeDescription(a.Description), req)
			}
		}
	} else {
		// Show child names
		parts := strings.Split(raw, ".")
		prefix := ""
		parentSegments := parts

		// If the last part is empty (trailing dot), we're listing children
		if len(parts) > 0 && parts[len(parts)-1] == "" {
			parentSegments = parts[:len(parts)-1]
		} else if len(parts) > 1 {
			prefix = parts[len(parts)-1]
			parentSegments = parts[:len(parts)-1]
		} else {
			// Single segment, no dot — list root children with this prefix
			prefix = parts[0]
			parentSegments = nil
		}

		parentID := rootID
		if len(parentSegments) > 0 {
			resolver := newCLIResolver(rootID)
			if resolver == nil {
				fmt.Println("Cannot load identity or store.")
				return nil
			}

			ctx, cancel := context.WithTimeout(context.Background(), naming.DefaultCompletionTimeout)
			defer cancel()

			var err error
			parentID, err = resolver.ResolveName(ctx, parentSegments)
			if err != nil {
				fmt.Printf("Cannot resolve %s: %v\n", strings.Join(parentSegments, "."), err)
				return nil
			}
		}

		resolver := newCLIResolver(rootID)
		if resolver == nil {
			fmt.Println("Cannot load identity or store.")
			return nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), naming.DefaultCompletionTimeout)
		defer cancel()

		entries, err := resolver.ListChildren(ctx, parentID, prefix)
		if err != nil {
			fmt.Printf("Cannot list children: %v\n", err)
			return nil
		}

		parentName := "root"
		if len(parentSegments) > 0 {
			parentName = "cf://" + strings.Join(parentSegments, ".")
		}
		fmt.Fprintf(w, "Names under %s:\n\n", parentName)
		if len(entries) == 0 {
			fmt.Fprintln(w, "  (no registered names)")
		}
		for _, e := range entries {
			desc := naming.SanitizeDescription(e.Description)
			fmt.Fprintf(w, "  %s\t%s\n", e.Name, desc)
		}
	}

	w.Flush()
	return nil
}

func init() {
	registerCompletions()
}
