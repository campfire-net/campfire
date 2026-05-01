package cmd

// argv0.go — argv[0] dispatch and per-app config overlay.
//
// When the cf binary is invoked under a different name (e.g. via a symlink),
// it operates as a multicall binary: the binary name becomes the campfire
// namespace prefix and convention operations are dispatched against it.
//
//   ln -s cf social
//   social lobby list        → cf social lobby list (convention dispatch)
//   social --version         → version info
//
// Per-app config overlay (design v2 §5.3, §F3):
//   When invoked as <appname>, loadConfigWithApp also reads
//   ~/.cf/apps/<appname>/config.toml as the highest-priority global-tier layer.
//   This lets each app have its own identity.display_name, transport defaults,
//   or naming seeds without editing the shared global config.
//
//   Layer order (lowest → highest priority):
//     1. compiled-in defaults
//     2. ~/.cf/config.toml              (global)
//     3. ~/.cf/apps/<app>/config.toml   (app overlay — global-tier only)
//     4. ancestor .cf/config.toml       (walk-up intermediates)
//     5. ./.cf/config.toml             (project)

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// primitiveCommandNames lists the protocol-primitive commands that are hidden
// from `cf --help` but remain executable. These live in cf-primitives; cf hides
// them so agents see only the convention surface.
var primitiveCommandNames = []string{
	"send", "read", "await", "inspect", "compact", "dm", "bridge",
	"filter", "sync", "nat-poll", "serve", "dag", "provenance",
}

// assignPrimitiveCommands marks protocol-primitive commands as hidden in the
// cf binary. Called from Execute() alongside assignCommandGroups().
func assignPrimitiveCommands() {
	for _, sub := range rootCmd.Commands() {
		for _, name := range primitiveCommandNames {
			if sub.Name() == name {
				sub.Hidden = true
				break
			}
		}
	}
}

// isMulticallInvocation returns true when the binary was invoked under a name
// that is not "cf" or "cf-primitives". The check uses the base name of the
// binary path, stripping any directory component.
//
// IsMulticallInvocation is the exported alias used by main.go.
func isMulticallInvocation(binaryPath string) bool {
	if binaryPath == "" {
		return false
	}
	name := filepath.Base(binaryPath)
	// filepath.Base("") returns "." — treat that as not a multicall.
	if name == "." || name == "" {
		return false
	}
	// Remove platform extensions (.exe on Windows).
	if idx := strings.LastIndex(name, "."); idx > 0 {
		ext := name[idx:]
		if ext == ".exe" {
			name = name[:idx]
		}
	}
	return name != "cf" && name != "cf-primitives"
}

// IsMulticallInvocation is the exported form of isMulticallInvocation for use
// by main.go. See isMulticallInvocation for documentation.
func IsMulticallInvocation(binaryPath string) bool {
	return isMulticallInvocation(binaryPath)
}

// sanitizeBinaryName strips any characters from the binary base-name that are
// not safe for use as a campfire namespace prefix. Retains [a-zA-Z0-9._-].
// Returns the sanitized name, or an empty string if nothing safe remains.
//
// SanitizeBinaryName is the exported alias used by main.go.
func sanitizeBinaryName(name string) string {
	var b strings.Builder
	for _, ch := range filepath.Base(name) {
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '-' || ch == '_' || ch == '.' {
			b.WriteRune(ch)
		}
	}
	s := b.String()
	// Reject names that begin with a dot (hidden file convention) or hyphen
	// to avoid confusion with flags.
	if len(s) > 0 && (s[0] == '.' || s[0] == '-') {
		return ""
	}
	return s
}

// SanitizeBinaryName is the exported form of sanitizeBinaryName for use by main.go.
func SanitizeBinaryName(name string) string {
	return sanitizeBinaryName(name)
}

// loadConfigWithApp loads the configuration cascade and, when appName is
// non-empty, inserts the per-app global overlay
// (~/.cf/apps/<appName>/config.toml) as a layer with source "app".
// The app layer sits between the global config and the walk-up ancestors,
// giving app-specific settings lower priority than project config but higher
// than the base global config.
//
// The returned Config, layers, warnings, and error have the same semantics
// as protocol.LoadConfig.
func loadConfigWithApp(globalDir, projectDir, appName string) (*protocol.Config, []protocol.ConfigLayer, []string, error) {
	// Use the standard loader for the base cascade.
	cfg, layers, warnings, err := protocol.LoadConfig(globalDir, projectDir)
	if err != nil || appName == "" {
		return cfg, layers, warnings, err
	}

	// Locate the per-app config overlay.
	appConfigPath := filepath.Join(globalDir, "apps", appName, "config.toml")
	if _, statErr := os.Stat(appConfigPath); statErr != nil {
		// File absent — nothing to overlay.
		return cfg, layers, warnings, nil
	}

	// Load the app config layer using the protocol-internal loader.
	// We use the exported protocol.LoadConfig with a synthetic single-dir tree
	// so the security checks (S1–S6) are applied to the app config file.
	// The trick: pass appConfigDir as both globalDir and projectDir so only the
	// global-tier file is loaded, then merge it into our running Config.
	appConfigDir := filepath.Dir(appConfigPath)
	appCfg, appLayers, appWarnings, appErr := protocol.LoadConfig(appConfigDir, appConfigDir)
	warnings = append(warnings, appWarnings...)
	if appErr != nil {
		return cfg, layers, warnings, appErr
	}

	// Find the layer that LoadConfig loaded (there should be at most one, the
	// global layer at appConfigDir/config.toml).
	for i := range appLayers {
		if appLayers[i].Source == "global" {
			appLayers[i].Source = "app"
			appLayers[i].Path = appConfigPath
		}
	}

	// Merge the app settings into cfg. App config is lower priority than
	// ancestor/project layers, so we replay the merge order.
	//
	// Implementation: Re-run LoadConfig with the app overlay injected.
	// We do this by delegating to the re-entrant loader below.
	return loadConfigWithAppOverlay(globalDir, projectDir, appName, appCfg, appLayers)
}

// loadConfigWithAppOverlay is the internal implementation that re-runs the full
// cascade with the app layer inserted between global and walk-up ancestors.
func loadConfigWithAppOverlay(globalDir, projectDir, appName string, appCfg *protocol.Config, appLayers []protocol.ConfigLayer) (*protocol.Config, []protocol.ConfigLayer, []string, error) {
	// Re-load the standard cascade.
	cfg, layers, warnings, err := protocol.LoadConfig(globalDir, projectDir)
	if err != nil {
		return cfg, layers, warnings, err
	}

	// The app config values that differ from compiled defaults should override
	// values that came solely from the global config, but be overridden by
	// project/ancestor values. Since protocol.LoadConfig has already applied
	// all layers, we can't easily re-insert the app layer in the middle.
	//
	// Strategy: Identify which fields the app config provides. For each such
	// field, check whether a more specific layer (ancestor or project) has
	// already set it. If not, apply the app config's value.
	//
	// We detect "more specific than global" by looking for layers with source
	// "ancestor" or "project" that contributed the same field.
	appContributed := appLayerFields(appCfg)

	specificFields := make(map[string]bool)
	for _, l := range layers {
		if l.Source == "ancestor" || l.Source == "project" {
			for _, f := range l.Fields {
				specificFields[f] = true
			}
		}
	}

	// Apply app config values that are NOT already overridden by a more specific layer.
	for _, field := range appContributed {
		if !specificFields[field] {
			protocol.ApplyConfigField(cfg, appCfg, field)
		}
	}

	// Insert the app layers into the layers list after the global layer.
	var globalIdx int
	for i, l := range layers {
		if l.Source == "global" {
			globalIdx = i + 1
			break
		}
	}
	// Annotate app layers with contributed fields.
	for i := range appLayers {
		appLayers[i].Fields = appContributed
	}
	// Insert app layers after global.
	merged := make([]protocol.ConfigLayer, 0, len(layers)+len(appLayers))
	merged = append(merged, layers[:globalIdx]...)
	merged = append(merged, appLayers...)
	merged = append(merged, layers[globalIdx:]...)

	return cfg, merged, warnings, nil
}

// appLayerFields returns the list of field names present in appCfg that differ
// from protocol's compiled-in defaults.
func appLayerFields(app *protocol.Config) []string {
	defaults := protocol.DefaultConfig()
	var fields []string
	if app.Identity.DisplayName != defaults.Identity.DisplayName {
		fields = append(fields, "identity.display_name")
	}
	if app.Identity.File != defaults.Identity.File {
		fields = append(fields, "identity.file")
	}
	if app.Identity.Backend != defaults.Identity.Backend {
		fields = append(fields, "identity.backend")
	}
	if app.Identity.Fingerprint != defaults.Identity.Fingerprint {
		fields = append(fields, "identity.fingerprint")
	}
	if app.Store.File != defaults.Store.File {
		fields = append(fields, "store.file")
	}
	if app.Transport.Type != defaults.Transport.Type {
		fields = append(fields, "transport.type")
	}
	if app.Transport.Endpoint != defaults.Transport.Endpoint {
		fields = append(fields, "transport.endpoint")
	}
	if app.Transport.Dir != defaults.Transport.Dir {
		fields = append(fields, "transport.dir")
	}
	if app.Naming.Root != defaults.Naming.Root {
		fields = append(fields, "naming.root")
	}
	if len(app.Naming.Seeds) > 0 {
		fields = append(fields, "naming.seeds")
	}
	if len(app.Behavior.AutoJoin) > 0 {
		fields = append(fields, "behavior.auto_join")
	}
	return fields
}
