// Demo: tag-prefix denylist as parameter (campfireagent-28f, leak #6 closure).
//
// Prints the convention parser's accepted prefix denylist and demonstrates
// that the parser accepts a caller-supplied list instead of hard-coding
// pkg/naming.TagPrefix and cf-protocol/campfire.TagPrefix imports.
//
// Run from repo root: go run ./cf-conventions/demos/prefix_param_demo/
package main

import (
	"encoding/json"
	"fmt"

	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

func mustPayload(m map[string]any) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

func main() {
	fmt.Println("=== tag-prefix denylist as parameter (campfireagent-28f) ===")
	fmt.Println()
	fmt.Println("§9 Leak #6 closure: the convention parser (L2 machinery) no longer")
	fmt.Println("imports pkg/naming or cf-protocol/campfire for tag-prefix constants.")
	fmt.Println()
	fmt.Println("--- DefaultDeniedTagPrefixes (from parser.go) ---")
	for i, p := range convention.DefaultDeniedTagPrefixes {
		fmt.Printf("  [%d] %q\n", i, p)
	}
	fmt.Println()

	// Adversarial 1: naming:foo denied by default list
	payload1 := mustPayload(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"produces_tags": []any{
			map[string]any{"tag": "naming:foo", "cardinality": "exactly_one"},
		},
	})
	_, _, err := convention.Parse(
		[]string{convention.ConventionOperationTag},
		payload1, "sender", "campfire",
		convention.DefaultDeniedTagPrefixes,
	)
	if err != nil {
		fmt.Printf("PASS: naming:foo correctly denied by DefaultDeniedTagPrefixes: %v\n", err)
	} else {
		fmt.Println("FAIL: naming:foo should have been denied")
	}

	// Adversarial 2: naming:foo allowed with nil prefix list
	_, _, err = convention.Parse(
		[]string{convention.ConventionOperationTag},
		payload1, "sender", "campfire",
		nil, // empty: suppress all prefix-based denials
	)
	if err == nil {
		fmt.Println("PASS: naming:foo allowed with nil prefix list (all prefix denials suppressed)")
	} else {
		fmt.Printf("FAIL: unexpected error with nil prefix list: %v\n", err)
	}

	// Adversarial 3: custom prefix list
	customPayload := mustPayload(map[string]any{
		"convention": "c", "version": "1", "operation": "op", "signing": "member_key",
		"produces_tags": []any{
			map[string]any{"tag": "custom:tag", "cardinality": "exactly_one"},
		},
	})
	_, _, err = convention.Parse(
		[]string{convention.ConventionOperationTag},
		customPayload, "sender", "campfire",
		[]string{"custom:"},
	)
	if err != nil {
		fmt.Printf("PASS: custom: prefix denied by caller-supplied list: %v\n", err)
	} else {
		fmt.Println("FAIL: custom:tag should have been denied")
	}

	// Adversarial 4: naming-uri exception still works with default list
	namingPayload := mustPayload(map[string]any{
		"convention": "naming-uri", "version": "1", "operation": "register", "signing": "member_key",
		"produces_tags": []any{
			map[string]any{"tag": "naming:name:foo", "cardinality": "exactly_one"},
		},
	})
	_, _, err = convention.Parse(
		[]string{convention.ConventionOperationTag},
		namingPayload, "sender", "campfire",
		convention.DefaultDeniedTagPrefixes,
	)
	if err == nil {
		fmt.Println("PASS: naming-uri exception still allows naming: tags")
	} else {
		fmt.Printf("FAIL: naming-uri exception broken: %v\n", err)
	}

	fmt.Println()
	fmt.Println("=== Demo complete ===")
	fmt.Println()
	fmt.Println("Key change summary:")
	fmt.Println("  Before: var deniedTagPrefixes = []string{naming.TagPrefix, campfire.TagPrefix}")
	fmt.Println("  After:  var DefaultDeniedTagPrefixes = []string{\"naming:\", \"campfire:\"}")
	fmt.Println("          func Parse(..., deniedPrefixes []string)")
	fmt.Println()
	fmt.Println("pkg/naming and cf-protocol/campfire are NO LONGER imported by parser.go (L2).")
}
