package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/provenance"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// verifyCmd implements cf verify <key-or-name> [--revoke].
//
// Refs: Operator Provenance Convention v0.1 §5, §12, §13.1.
var verifyCmd = &cobra.Command{
	Use:   "verify <key-or-name>",
	Short: "Verify an operator identity via challenge/response",
	Long:  "Initiate an operator provenance verification exchange.\n\nThe runtime sequences: challenge, wait, validate, store attestation, report level.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		keyOrName := args[0]
		revoke, _ := cmd.Flags().GetBool("revoke")
		contactCampfire, _ := cmd.Flags().GetString("contact-campfire")
		timeout, _ := cmd.Flags().GetDuration("timeout")

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		if revoke {
			return runVerifyRevoke(keyOrName, agentID.PublicKeyHex(), s)
		}

		return runVerifyChallenge(keyOrName, contactCampfire, agentID.PublicKeyHex(), timeout, s)
	},
}

// runVerifyChallenge runs the initiator side of the verification flow.
func runVerifyChallenge(keyOrName, contactCampfireHint, initiatorKey string, timeout time.Duration, s store.Store) error {
	targetKey, err := resolveOperatorKey(keyOrName, s)
	if err != nil {
		return fmt.Errorf("resolving operator key: %w", err)
	}

	contactCampfireID, err := resolveContactCampfire(targetKey, contactCampfireHint, s)
	if err != nil {
		return fmt.Errorf("resolving contact campfire: %w", err)
	}

	callbackCampfireID, err := resolveCallbackCampfire(s)
	if err != nil {
		return fmt.Errorf("resolving callback campfire: %w", err)
	}

	challenger := provenance.NewChallenger()
	challengeID := uuid.New().String()
	ch, err := challenger.IssueChallenge(challengeID, initiatorKey, targetKey, callbackCampfireID, time.Now())
	if err != nil {
		return fmt.Errorf("generating challenge: %w", err)
	}

	challengePayload := map[string]interface{}{
		"convention":        "operator-provenance",
		"operation":         "operator-challenge",
		"target_key":        ch.TargetKey,
		"nonce":             ch.Nonce,
		"callback_campfire": ch.CallbackCampfire,
	}
	payloadBytes, err := json.Marshal(challengePayload)
	if err != nil {
		return fmt.Errorf("marshaling challenge: %w", err)
	}

	contactMembership, err := s.GetMembership(contactCampfireID)
	if err != nil || contactMembership == nil {
		return fmt.Errorf("not a member of contact campfire %s", contactCampfireID[:12])
	}

	id, err := loadIdentity()
	if err != nil {
		return fmt.Errorf("loading identity: %w", err)
	}
	client := protocol.New(s, id)
	sentMsg, err := client.Send(protocol.SendRequest{
		CampfireID: contactCampfireID,
		Payload:    payloadBytes,
		Tags:       []string{"provenance:challenge"},
	})
	if err != nil {
		return fmt.Errorf("sending challenge: %w", err)
	}

	fmt.Printf("Challenge sent (msg: %s)\n", sentMsg.ID[:12])
	fmt.Printf("Waiting for response in %s (timeout: %s)...\n", callbackCampfireID[:12], timeout)

	resp, err := waitForVerifyResponse(ch, callbackCampfireID, timeout, s)
	if err != nil {
		return fmt.Errorf("waiting for response: %w", err)
	}

	validatedChallenge, err := challenger.ValidateResponse(resp, time.Now())
	if err != nil {
		return fmt.Errorf("invalid response: %w", err)
	}

	attestationStore := loadProvenanceStore()
	attestationID := uuid.New().String()
	attestation, storeErr := provenance.CreateAttestation(attestationStore, attestationID, validatedChallenge, resp, time.Now())
	if storeErr != nil && storeErr != provenance.ErrNotCoSigned {
		return fmt.Errorf("storing attestation: %w", storeErr)
	}

	level := attestationStore.Level(targetKey)
	if attestation != nil && !attestation.CoSigned {
		fmt.Printf("Warning: attestation is not co-signed (accepted at reduced trust)\n")
	}
	fmt.Printf("Operator verified at level %d (%s)\n", level, level)
	fmt.Printf("Attestation ID: %s\n", attestationID)

	return nil
}

// runVerifyRevoke revokes the most recent attestation for a target key.
func runVerifyRevoke(keyOrName, initiatorKey string, s store.Store) error {
	targetKey, err := resolveOperatorKey(keyOrName, s)
	if err != nil {
		return fmt.Errorf("resolving operator key: %w", err)
	}

	attestationStore := loadProvenanceStore()
	attestations := attestationStore.Attestations(targetKey)
	if len(attestations) == 0 {
		return fmt.Errorf("no attestations found for %s", keyOrName)
	}

	var toRevoke *provenance.Attestation
	for i := len(attestations) - 1; i >= 0; i-- {
		a := attestations[i]
		if a.VerifierKey == initiatorKey {
			toRevoke = a
			break
		}
	}
	if toRevoke == nil {
		return fmt.Errorf("no attestations by your key found for %s", keyOrName)
	}

	if err := attestationStore.Revoke(toRevoke.ID); err != nil {
		return fmt.Errorf("revoking attestation: %w", err)
	}

	level := attestationStore.Level(targetKey)
	fmt.Printf("Attestation %s revoked\n", toRevoke.ID[:12])
	fmt.Printf("Operator %s is now at level %d (%s)\n", keyOrName, level, level)

	return nil
}

// resolveOperatorKey resolves a key-or-name to a public key string.
func resolveOperatorKey(keyOrName string, s store.Store) (string, error) {
	// Key is at least 44 chars (base64 Ed25519) or 64 chars hex.
	if len(keyOrName) >= 44 && !strings.Contains(keyOrName, " ") {
		return keyOrName, nil
	}
	// Try as campfire ID prefix -> membership -> creator key.
	resolved, resolveErr := resolveCampfireID(keyOrName, s)
	if resolveErr == nil {
		m, err := s.GetMembership(resolved)
		if err == nil && m != nil && m.CreatorPubkey != "" {
			return m.CreatorPubkey, nil
		}
	}
	return "", fmt.Errorf("could not resolve operator key for %q", keyOrName)
}

// resolveContactCampfire resolves the operator contact campfire.
func resolveContactCampfire(targetKey, contactCampfireHint string, s store.Store) (string, error) {
	if contactCampfireHint != "" {
		return resolveCampfireID(contactCampfireHint, s)
	}
	shortKey := targetKey
	if len(shortKey) > 12 {
		shortKey = shortKey[:12]
	}
	return "", fmt.Errorf("could not auto-resolve contact campfire for key %s: use --contact-campfire", shortKey)
}

// resolveCallbackCampfire returns the campfire where we receive responses.
func resolveCallbackCampfire(s store.Store) (string, error) {
	memberships, err := s.ListMemberships()
	if err != nil {
		return "", fmt.Errorf("listing memberships: %w", err)
	}
	for _, m := range memberships {
		if m.Description == "home" || strings.Contains(strings.ToLower(m.Description), "home") {
			return m.CampfireID, nil
		}
	}
	if len(memberships) > 0 {
		return memberships[0].CampfireID, nil
	}
	return "", fmt.Errorf("no campfire membership found for callback")
}

// waitForVerifyResponse polls for a matching operator-verify response.
//
// Security: msg.Sender (the cryptographic signer of the campfire message) is
// checked against ch.TargetKey before parsing. Messages from other senders are
// silently skipped — a forged operator-verify from any other campfire member
// cannot be accepted as a valid response (campfire-agent-34c).
func waitForVerifyResponse(ch *provenance.Challenge, callbackCampfireID string, timeout time.Duration, s store.Store) (*provenance.ChallengeResponse, error) {
	deadline := time.Now().Add(timeout)
	pollInterval := 2 * time.Second

	for time.Now().Before(deadline) {
		msgs, err := s.ListMessages(callbackCampfireID, 0)
		if err == nil {
			for _, msg := range msgs {
				// Reject messages not signed by the target operator.
				// msg.Sender is hex-encoded Ed25519 public key from the
				// transport envelope — this is the cryptographic sender,
				// not a self-reported field.
				if msg.Sender != ch.TargetKey {
					continue
				}
				resp, ok := parseVerifyResponse(msg.Payload, ch)
				if ok {
					// Populate MessageSender so ValidateResponse can
					// perform its own envelope-sender check (campfire-agent-4bn).
					resp.MessageSender = msg.Sender
					return resp, nil
				}
			}
		}
		time.Sleep(pollInterval)
	}
	return nil, fmt.Errorf("no response received within %s", timeout)
}

// parseVerifyResponse attempts to parse a payload as an operator-verify response.
func parseVerifyResponse(payload []byte, ch *provenance.Challenge) (*provenance.ChallengeResponse, bool) {
	var m map[string]interface{}
	if err := json.Unmarshal(payload, &m); err != nil {
		return nil, false
	}
	op, _ := m["operation"].(string)
	if op != "operator-verify" {
		return nil, false
	}
	nonce, _ := m["nonce"].(string)
	if nonce != ch.Nonce {
		return nil, false
	}
	targetKey, _ := m["target_key"].(string)
	proofType, _ := m["proof_type"].(string)
	proofToken, _ := m["proof_token"].(string)
	proofProvenance, _ := m["proof_provenance"].(string)
	contactMethod, _ := m["contact_method"].(string)
	antecedent, _ := m["antecedent"].(string)

	return &provenance.ChallengeResponse{
		AntecedentID:    antecedent,
		ResponderKey:    targetKey,
		TargetKey:       targetKey,
		Nonce:           nonce,
		ContactMethod:   contactMethod,
		ProofType:       provenance.ProofType(proofType),
		ProofToken:      proofToken,
		ProofProvenance: proofProvenance,
		RespondedAt:     time.Now(),
	}, true
}

// loadProvenanceStore returns a file-backed attestation store persisted to
// ~/.campfire/attestations.json (or CF_HOME/attestations.json).
// Attestations survive process restarts — verified operator identities are
// recalled by subsequent cf verify and cf provenance show invocations.
// Falls back to an in-memory store if the file cannot be opened.
func loadProvenanceStore() provenance.AttestationStore {
	path := attestationStorePath()
	fs, err := provenance.NewFileStore(path, provenance.DefaultConfig())
	if err != nil {
		// Degrade gracefully: return in-memory store so the command still works.
		fmt.Fprintf(os.Stderr, "warning: could not open attestation store at %s: %v (attestations will not persist)\n", path, err)
		return provenance.NewStore(provenance.DefaultConfig())
	}
	return fs
}

// attestationStorePath returns the path to the persistent attestation store.
func attestationStorePath() string {
	return filepath.Join(CFHome(), "attestations.json")
}

func init() {
	verifyCmd.Flags().Bool("revoke", false, "revoke a prior attestation for this operator")
	verifyCmd.Flags().String("contact-campfire", "", "operator contact campfire ID (skip auto-resolve)")
	verifyCmd.Flags().Duration("timeout", 5*time.Minute, "timeout waiting for verification response")
	rootCmd.AddCommand(verifyCmd)
}
