package cmd

// github.go — shared helpers for the GitHub transport CLI integration.
//
// GitHub campfire state is stored in the TransportDir column of campfire_memberships as
// a JSON string prefixed with "github:" so it can be distinguished from filesystem and
// p2p-http campfires:
//
//	github:{"repo":"org/repo","issue_number":42}
//
// Token lookup order (design doc §7):
//  1. --github-token-env flag value (the name of an env var to read from)
//  2. GITHUB_TOKEN env var
//  3. ~/.campfire/github-token credential file
//  4. "gh auth token" (GitHub CLI, if available)

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/campfire-net/campfire/pkg/transport"
)

// githubTransportMeta holds the metadata needed to interact with a GitHub-transport
// campfire. It is stored as JSON in the TransportDir column (with the
// transport.GitHubTransportPrefix prefix) so the send/read commands can reconstruct
// the Transport without extra tables.
type githubTransportMeta struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
	BaseURL     string `json:"base_url,omitempty"` // empty = production GitHub
}

// encodeGitHubTransportDir serialises a githubTransportMeta into the TransportDir format.
func encodeGitHubTransportDir(meta githubTransportMeta) (string, error) {
	b, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("encoding github transport meta: %w", err)
	}
	return transport.GitHubTransportPrefix + string(b), nil
}

// parseGitHubTransportDir parses the TransportDir value for a GitHub-transport campfire.
// Returns (meta, true) if the value is a GitHub transport dir, (zero, false) otherwise.
func parseGitHubTransportDir(transportDir string) (githubTransportMeta, bool) {
	if !strings.HasPrefix(transportDir, transport.GitHubTransportPrefix) {
		return githubTransportMeta{}, false
	}
	raw := strings.TrimPrefix(transportDir, transport.GitHubTransportPrefix)
	var meta githubTransportMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return githubTransportMeta{}, false
	}
	return meta, true
}

// isGitHubCampfire returns true if the campfire's TransportDir indicates a GitHub-transport campfire.
func isGitHubCampfire(transportDir string) bool {
	_, ok := parseGitHubTransportDir(transportDir)
	return ok
}

// resolveGitHubToken returns the GitHub token using the priority order from the design doc.
// tokenEnvName is the value of --github-token-env (may be empty).
// cfHome is the campfire home directory (for reading ~/.campfire/github-token).
func resolveGitHubToken(tokenEnvName, cfHome string) (string, error) {
	// 1. --github-token-env: the flag value is an env var NAME to read.
	if tokenEnvName != "" {
		if tok := os.Getenv(tokenEnvName); tok != "" {
			return tok, nil
		}
		// Flag was given but the env var it points to was empty — fall through.
	}

	// 2. GITHUB_TOKEN env var.
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		return tok, nil
	}

	// 3. ~/.campfire/github-token credential file.
	credFile := filepath.Join(cfHome, "github-token")
	if data, err := os.ReadFile(credFile); err == nil {
		tok := strings.TrimSpace(string(data))
		if tok != "" {
			return tok, nil
		}
	}

	// 4. "gh auth token" (GitHub CLI, if available).
	out, err := exec.Command("gh", "auth", "token").Output()
	if err == nil {
		tok := strings.TrimSpace(string(out))
		if tok != "" {
			return tok, nil
		}
	}

	return "", fmt.Errorf("no GitHub token found: set GITHUB_TOKEN, use --github-token-env, write %s, or run 'gh auth login'", credFile)
}
