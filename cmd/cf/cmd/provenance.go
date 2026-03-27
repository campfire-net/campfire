package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/campfire/pkg/provenance"
	"github.com/spf13/cobra"
)

// provenanceCmd is the parent for operator provenance operations.
var provenanceCmd = &cobra.Command{
	Use:   "provenance",
	Short: "Inspect operator provenance levels and attestation history",
	Long:  "Inspect operator provenance state (local -- no messages sent).\n\n  cf provenance show <key>    display level, attestation history, freshness",
}

// provenanceShowCmd implements cf provenance show <key>.
//
// Refs: Operator Provenance Convention v0.1 §4, §6, §13.2.
var provenanceShowCmd = &cobra.Command{
	Use:   "show <key>",
	Short: "Display provenance level, attestation history, and freshness for a key",
	Long:  "Display local provenance state for an operator key. No messages sent.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		keyOrName := args[0]

		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		targetKey, err := resolveOperatorKey(keyOrName, s)
		if err != nil {
			targetKey = keyOrName
		}

		attestationStore := loadProvenanceStore()
		level := attestationStore.Level(targetKey)
		attestations := attestationStore.Attestations(targetKey)

		if jsonOutput {
			return provenanceShowJSON(targetKey, level, attestations)
		}

		return provenanceShowHuman(keyOrName, targetKey, level, attestations)
	},
}

// provenanceShowHuman prints the human-readable provenance report.
func provenanceShowHuman(name, key string, level provenance.Level, attestations []*provenance.Attestation) error {
	displayKey := key
	if len(displayKey) > 24 {
		displayKey = displayKey[:24] + "..."
	}

	fmt.Printf("Operator:  %s\n", name)
	fmt.Printf("Key:       %s\n", displayKey)
	fmt.Printf("Level:     %d (%s)\n\n", level, levelDescription(level))

	if len(attestations) == 0 {
		fmt.Println("Attestations: none")
		return nil
	}

	now := time.Now()
	cfg := provenance.DefaultConfig()

	fmt.Printf("Attestations (%d):\n", len(attestations))
	for i, a := range attestations {
		age := now.Sub(a.VerifiedAt)
		fresh := cfg.FreshnessWindow > 0 && age <= cfg.FreshnessWindow
		coSigned := "co-signed"
		if !a.CoSigned {
			coSigned = "not co-signed (reduced trust)"
		}
		freshStr := ""
		if fresh {
			freshStr = " [fresh]"
		} else {
			freshStr = fmt.Sprintf(" [stale: %s ago]", formatAge(age))
		}

		verifierShort := a.VerifierKey
		if len(verifierShort) > 16 {
			verifierShort = verifierShort[:16] + "..."
		}

		fmt.Printf("  %d. verified %s%s\n", i+1, a.VerifiedAt.UTC().Format("2006-01-02 15:04 UTC"), freshStr)
		fmt.Printf("     verifier:       %s\n", verifierShort)
		fmt.Printf("     contact:        %s\n", a.ContactMethod)
		fmt.Printf("     proof type:     %s\n", a.ProofType)
		fmt.Printf("     signing:        %s\n", coSigned)
		fmt.Printf("     attestation ID: %s\n", a.ID)
		fmt.Println()
	}

	return nil
}

// provenanceShowJSON prints the provenance report as JSON.
func provenanceShowJSON(key string, level provenance.Level, attestations []*provenance.Attestation) error {
	now := time.Now()
	cfg := provenance.DefaultConfig()

	type attestationJSON struct {
		ID            string `json:"id"`
		VerifierKey   string `json:"verifier_key"`
		ContactMethod string `json:"contact_method"`
		ProofType     string `json:"proof_type"`
		VerifiedAt    string `json:"verified_at"`
		AgeSeconds    int64  `json:"age_seconds"`
		Fresh         bool   `json:"fresh"`
		CoSigned      bool   `json:"co_signed"`
		Revoked       bool   `json:"revoked"`
	}

	var attestationList []attestationJSON
	for _, a := range attestations {
		age := now.Sub(a.VerifiedAt)
		fresh := cfg.FreshnessWindow > 0 && age <= cfg.FreshnessWindow
		attestationList = append(attestationList, attestationJSON{
			ID:            a.ID,
			VerifierKey:   a.VerifierKey,
			ContactMethod: a.ContactMethod,
			ProofType:     string(a.ProofType),
			VerifiedAt:    a.VerifiedAt.UTC().Format(time.RFC3339),
			AgeSeconds:    int64(age.Seconds()),
			Fresh:         fresh,
			CoSigned:      a.CoSigned,
			Revoked:       a.Revoked,
		})
	}

	out := map[string]interface{}{
		"key":          key,
		"level":        int(level),
		"level_name":   level.String(),
		"description":  levelDescription(level),
		"attestations": attestationList,
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// levelDescription returns the one-line description for a provenance level.
func levelDescription(l provenance.Level) string {
	switch l {
	case provenance.LevelAnonymous:
		return "no verification -- only a valid keypair"
	case provenance.LevelClaimed:
		return "self-asserted identity (tainted, unverified)"
	case provenance.LevelContactable:
		return "verified: a human responded to a challenge"
	case provenance.LevelPresent:
		return "verified and fresh: a human was recently present"
	default:
		return "unknown"
	}
}

// formatAge returns a human-readable age string.
func formatAge(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d.Hours() / 24)
	return fmt.Sprintf("%dd", days)
}

func init() {
	provenanceCmd.AddCommand(provenanceShowCmd)
	rootCmd.AddCommand(provenanceCmd)
}
