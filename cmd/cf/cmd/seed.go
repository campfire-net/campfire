package cmd

// seedCampfire posts the embedded promote declaration and any declarations
// found in the prioritized seed beacon search into a newly created campfire.
//
// The embedded promote declaration is always posted — it is the bootstrap
// primitive. Seed beacon declarations are opportunistic: seeding succeeds
// even when no beacon is found.
//
// transportDir is the campfire's filesystem directory (from membership record).
// agentID and cfCampfire are used to sign messages.
//
// Errors from seed operations are non-fatal: they are printed to stderr but
// do not prevent the campfire from being created.

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/seed"
)

// seedCampfireFilesystem seeds a newly created filesystem campfire with:
//  1. The embedded convention-extension:promote declaration (always).
//  2. Convention declarations from the prioritized seed beacon (if found).
//
// campfireID is the 64-char hex campfire public key.
// transportDir is the campfire's filesystem transport directory.
// agentID is the agent identity used to sign declaration messages.
// cf is the campfire whose campfire key signs the provenance hops.
// projectDir is the project root (used for priority-1 seed beacon lookup).
//
// All errors are non-fatal: they are logged to stderr but do not abort.
func seedCampfireFilesystem(
	campfireID string,
	transportDir string,
	agentID *identity.Identity,
	cf *campfire.Campfire,
	projectDir string,
) {
	// Step 1: Post embedded promote declaration (always, unconditionally).
	promoteDecl := convention.PromoteDeclaration()
	promotePayload, err := json.Marshal(promoteDecl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to marshal promote declaration: %v\n", err)
	} else {
		tags := []string{"convention:operation"}
		if _, err := sendFilesystem(campfireID, string(promotePayload), tags, nil, "", agentID, transportDir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to post promote declaration: %v\n", err)
		}
	}

	// Step 2: Scan for seed beacon (priority: project > user > system > network).
	sb, err := seed.FindSeedBeacon(projectDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: seed beacon search failed: %v\n", err)
		return
	}
	if sb == nil {
		return // no seed beacon found — promote-only fallback complete
	}

	// Step 3: Read convention:operation messages from the seed campfire.
	msgs, err := seed.ReadConventionMessages(sb)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: reading seed campfire failed: %v\n", err)
		return
	}

	// Step 4: Copy declarations into the new campfire.
	for _, msg := range msgs {
		if _, err := sendFilesystem(campfireID, string(msg.Payload), msg.Tags, nil, "", agentID, transportDir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to copy seed declaration: %v\n", err)
		}
	}
}
