package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PolicyPreset names supported by cf init --policy.
const (
	PolicyPresetPersonalDeveloper = "personal-developer"
	PolicyPresetTeamMember        = "team-member"
	PolicyPresetPublicAgent       = "public-agent"
)

// grantCapability represents a single capability in a grant template.
// It covers the convention, op pattern, optional where-matcher, and
// default TTL for automatic grants under this preset.
type grantCapability struct {
	Convention string `json:"convention"`
	OpGlob     string `json:"op_glob"`
	Where      string `json:"where,omitempty"` // empty = "wherever agent is a member"
	DefaultTTL string `json:"default_ttl"`     // e.g. "7d", "24h"
}

// PolicyTemplate is the JSON written to grant-template.json by cf init --policy.
// It documents the preset name, delegation depth, auto-grant TTL, and the
// set of capabilities pre-approved for automatic-grant issuance.
type PolicyTemplate struct {
	Preset     string            `json:"preset"`
	MaxDepth   int               `json:"max_depth"`
	AutoTTL    string            `json:"auto_ttl"`    // TTL for automatic grants
	OwnerTTL   string            `json:"owner_ttl"`   // TTL for owner-approved grants
	MultiOwner bool              `json:"multi_owner"` // true for team-member (threshold signing)
	Caps       []grantCapability `json:"capabilities"`
	Comment    string            `json:"comment"`
}

const grantTemplateFile = "grant-template.json"

// validPresets is the set of supported preset names for user-facing validation.
var validPresets = map[string]bool{
	PolicyPresetPersonalDeveloper: true,
	PolicyPresetTeamMember:        true,
	PolicyPresetPublicAgent:       true,
}

// policyTemplateFor returns the PolicyTemplate for the given preset name.
// Returns an error for unknown presets.
func policyTemplateFor(preset string) (*PolicyTemplate, error) {
	switch preset {
	case PolicyPresetPersonalDeveloper:
		return personalDeveloperTemplate(), nil
	case PolicyPresetTeamMember:
		return teamMemberTemplate(), nil
	case PolicyPresetPublicAgent:
		return publicAgentTemplate(), nil
	default:
		return nil, fmt.Errorf(
			"unknown policy preset %q — valid values: personal-developer, team-member, public-agent",
			preset,
		)
	}
}

// personalDeveloperTemplate is for single-operator solo use.
// Single owner, no delegation, 7-day auto-grants, full rd/dontguess/social/reach access.
func personalDeveloperTemplate() *PolicyTemplate {
	return &PolicyTemplate{
		Preset:     PolicyPresetPersonalDeveloper,
		MaxDepth:   1,
		AutoTTL:    "24h",
		OwnerTTL:   "7d",
		MultiOwner: false,
		Caps: []grantCapability{
			{Convention: "rd", OpGlob: "*", DefaultTTL: "7d"},
			{Convention: "dontguess", OpGlob: "buy|put|get", DefaultTTL: "7d"},
			{Convention: "social", OpGlob: "post|reply|dm", DefaultTTL: "7d"},
			{Convention: "reach", OpGlob: "publish|discover|subscribe", DefaultTTL: "7d"},
		},
		Comment: "Single-operator developer identity. No delegation. Full rd/dontguess/social/reach access. Automatic grants expire in 24h; owner-approved grants last 7d.",
	}
}

// teamMemberTemplate is for multi-operator setups with threshold signing and delegation.
// Two-person threshold, delegation enabled (depth 2), tighter auto-grant TTLs.
func teamMemberTemplate() *PolicyTemplate {
	return &PolicyTemplate{
		Preset:     PolicyPresetTeamMember,
		MaxDepth:   2,
		AutoTTL:    "24h",
		OwnerTTL:   "7d",
		MultiOwner: true,
		Caps: []grantCapability{
			{Convention: "rd", OpGlob: "claim|progress|done|fail|cancel", DefaultTTL: "24h"},
			{Convention: "dontguess", OpGlob: "buy|put|get", DefaultTTL: "24h"},
			{Convention: "social", OpGlob: "post|reply|dm", DefaultTTL: "24h"},
			{Convention: "reach", OpGlob: "publish|discover|subscribe", DefaultTTL: "24h"},
		},
		Comment: "Team member identity. Multi-owner threshold signing enabled. Delegation depth 2 (human → agent → worker). Automatic grants expire in 24h.",
	}
}

// publicAgentTemplate is for public-facing hosted agents (e.g. hosted MCP service).
// Minimal capabilities, tight TTLs, no delegation, read-heavy access pattern.
func publicAgentTemplate() *PolicyTemplate {
	return &PolicyTemplate{
		Preset:     PolicyPresetPublicAgent,
		MaxDepth:   1,
		AutoTTL:    "24h",
		OwnerTTL:   "24h",
		MultiOwner: false,
		Caps: []grantCapability{
			{Convention: "rd", OpGlob: "claim|progress|done", DefaultTTL: "24h"},
			{Convention: "dontguess", OpGlob: "buy|get", DefaultTTL: "24h"},
			{Convention: "social", OpGlob: "post|reply", DefaultTTL: "24h"},
			{Convention: "reach", OpGlob: "publish|discover", DefaultTTL: "24h"},
		},
		Comment: "Public-facing hosted agent. Minimal capability set. No delegation. Tight 24h TTLs on both automatic and owner-approved grants. Suitable for hosted MCP service.",
	}
}

// writeGrantTemplate serializes the PolicyTemplate as JSON and writes it to
// <cfHome>/grant-template.json with 0600 permissions.
func writeGrantTemplate(cfHome string, tmpl *PolicyTemplate) error {
	data, err := json.MarshalIndent(tmpl, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling grant template: %w", err)
	}
	if err := os.MkdirAll(cfHome, 0700); err != nil {
		return fmt.Errorf("creating campfire home: %w", err)
	}
	path := filepath.Join(cfHome, grantTemplateFile)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing grant template: %w", err)
	}
	return nil
}

// applyPolicyPreset resolves the preset, writes grant-template.json, and returns
// a short human-readable summary of what was written.
func applyPolicyPreset(cfHome, preset string) (string, error) {
	tmpl, err := policyTemplateFor(preset)
	if err != nil {
		return "", err
	}
	if err := writeGrantTemplate(cfHome, tmpl); err != nil {
		return "", err
	}
	return fmt.Sprintf("policy preset %q applied: %s", preset, tmpl.Comment), nil
}
