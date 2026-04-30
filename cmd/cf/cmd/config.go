package cmd

// config.go — cf config subcommands: list, get, set, layers.
//
// cf config list [--show-origin] [--json]
// cf config get <key>
// cf config set <key> <value> [--global|--project|--local]
// cf config layers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect and modify campfire configuration",
	Long: `Inspect and modify campfire configuration.

  cf config list [--show-origin] [--json]  show resolved config
  cf config get <key>                       print a single value
  cf config set <key> <value> [--global|--project|--local]  write a value
  cf config layers                          show discovered config files`,
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show the resolved config with optional source annotations",
	RunE: func(cmd *cobra.Command, args []string) error {
		showOrigin, _ := cmd.Flags().GetBool("show-origin")
		useJSON, _ := cmd.Flags().GetBool("json")

		globalDir := CFHome()
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "."
		}
		cfg, layers, warnings, err := protocol.LoadConfig(globalDir, cwd)
		if err != nil {
			return err
		}
		for _, w := range warnings {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}

		fields := configFieldsWithOrigin(cfg, layers)

		if useJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(fields)
		}

		for _, f := range fields {
			if showOrigin {
				fmt.Fprintf(cmd.OutOrStdout(), "%-30s = %-35s (%s: %s)\n",
					f["key"], f["value"], f["source"], f["file"])
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "%-30s = %s\n", f["key"], f["value"])
			}
		}
		return nil
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Print a single config value",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]

		globalDir := CFHome()
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "."
		}
		cfg, _, _, err := protocol.LoadConfig(globalDir, cwd)
		if err != nil {
			return err
		}

		val, err := configGetValue(cfg, key)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), val)
		return nil
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Write a config value to a specific layer",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		value := args[1]

		useGlobal, _ := cmd.Flags().GetBool("global")
		useProject, _ := cmd.Flags().GetBool("project")
		useLocal, _ := cmd.Flags().GetBool("local")

		// Determine target file.
		var targetPath string
		switch {
		case useGlobal:
			targetPath = filepath.Join(CFHome(), "config.toml")
		case useProject, useLocal:
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getting working directory: %w", err)
			}
			targetPath = filepath.Join(cwd, ".cf", "config.toml")
		default:
			// Default to global.
			targetPath = filepath.Join(CFHome(), "config.toml")
		}

		return configSetValue(targetPath, key, value)
	},
}

var configLayersCmd = &cobra.Command{
	Use:   "layers",
	Short: "Show all discovered config layers and their contributions",
	RunE: func(cmd *cobra.Command, args []string) error {
		useJSON, _ := cmd.Flags().GetBool("json")

		globalDir := CFHome()
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "."
		}
		_, layers, warnings, err := protocol.LoadConfig(globalDir, cwd)
		if err != nil {
			return err
		}
		for _, w := range warnings {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}

		if len(layers) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No config files found.")
			return nil
		}

		if useJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(layers)
		}

		for i, layer := range layers {
			status := ""
			if layer.Skipped {
				status = fmt.Sprintf(" [SKIPPED: %s]", layer.SkipReason)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Layer %d: %s (%s)%s\n", i+1, layer.Path, layer.Source, status)
			if !layer.Skipped {
				if len(layer.Fields) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "  (no fields contributed)")
				}
				for _, field := range layer.Fields {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", field)
				}
			}
		}
		return nil
	},
}

func init() {
	configListCmd.Flags().Bool("show-origin", false, "annotate each value with its source config file")
	configListCmd.Flags().Bool("json", false, "output as JSON")

	configLayersCmd.Flags().Bool("json", false, "output as JSON")

	configSetCmd.Flags().Bool("global", false, "write to global config (~/.cf/config.toml)")
	configSetCmd.Flags().Bool("project", false, "write to project config (./.cf/config.toml)")
	configSetCmd.Flags().Bool("local", false, "alias for --project")

	configCmd.AddCommand(configListCmd)
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configLayersCmd)
	rootCmd.AddCommand(configCmd)
}

// configFieldsWithOrigin returns all config fields as a list of maps with
// key, value, source, and file annotations derived from the resolved Config
// and layer list.
func configFieldsWithOrigin(cfg *protocol.Config, layers []protocol.ConfigLayer) []map[string]string {
	// Build a map from field name → (source, file) from the layers.
	// Later layers overwrite earlier ones (deepest wins for scalars).
	fieldOrigin := make(map[string]struct{ source, file string })
	for _, layer := range layers {
		if layer.Skipped {
			continue
		}
		for _, field := range layer.Fields {
			fieldOrigin[field] = struct{ source, file string }{layer.Source, layer.Path}
		}
	}

	origin := func(key string) (string, string) {
		if o, ok := fieldOrigin[key]; ok {
			return o.source, o.file
		}
		return "default", "(compiled-in)"
	}

	type entry struct {
		key   string
		value string
	}

	entries := []entry{
		{"identity.file", cfg.Identity.File},
		{"identity.display_name", cfg.Identity.DisplayName},
		{"identity.present_as", cfg.Identity.PresentAs},
		{"store.file", cfg.Store.File},
		{"transport.type", cfg.Transport.Type},
		{"transport.endpoint", cfg.Transport.Endpoint},
		{"transport.dir", cfg.Transport.Dir},
		{"naming.root", cfg.Naming.Root},
		{"naming.seeds", formatStringSlice(cfg.Naming.Seeds)},
		{"behavior.walk_up", fmt.Sprintf("%v", cfg.Behavior.WalkUp)},
		{"behavior.auto_join", formatStringSlice(cfg.Behavior.AutoJoin)},
	}

	var result []map[string]string
	for _, e := range entries {
		src, file := origin(e.key)
		result = append(result, map[string]string{
			"key":    e.key,
			"value":  e.value,
			"source": src,
			"file":   file,
		})
	}
	return result
}

// configGetValue returns the string value of a dot-separated config key.
func configGetValue(cfg *protocol.Config, key string) (string, error) {
	switch key {
	case "identity.file":
		return cfg.Identity.File, nil
	case "identity.display_name":
		return cfg.Identity.DisplayName, nil
	case "identity.present_as":
		return cfg.Identity.PresentAs, nil
	case "store.file":
		return cfg.Store.File, nil
	case "transport.type":
		return cfg.Transport.Type, nil
	case "transport.endpoint":
		return cfg.Transport.Endpoint, nil
	case "transport.dir":
		return cfg.Transport.Dir, nil
	case "naming.root":
		return cfg.Naming.Root, nil
	case "naming.seeds":
		return formatStringSlice(cfg.Naming.Seeds), nil
	case "behavior.walk_up":
		return fmt.Sprintf("%v", cfg.Behavior.WalkUp), nil
	case "behavior.auto_join":
		return formatStringSlice(cfg.Behavior.AutoJoin), nil
	default:
		return "", fmt.Errorf("unknown config key %q; valid keys: identity.file, identity.display_name, identity.present_as, store.file, transport.type, transport.endpoint, transport.dir, naming.root, naming.seeds, behavior.walk_up, behavior.auto_join", key)
	}
}

// configSetValue reads the TOML at targetPath (or starts fresh), updates the
// given key, and writes back. Creates parent directories if needed.
func configSetValue(targetPath, key, value string) error {
	// Validate key before touching the file.
	validKeys := map[string]bool{
		"identity.file": true, "identity.display_name": true, "identity.present_as": true,
		"store.file":        true,
		"transport.type":    true, "transport.endpoint": true, "transport.dir": true,
		"naming.root":       true, "naming.seeds": true,
		"behavior.walk_up":  true, "behavior.auto_join": true,
	}
	if !validKeys[key] {
		return fmt.Errorf("unknown config key %q; use cf config list to see valid keys", key)
	}

	// Reject scope.* writes (design doc §Change 4 red line).
	if strings.HasPrefix(key, "scope.") {
		return fmt.Errorf("config key %q is immutable; scope is set by the operator, not by cf config set", key)
	}

	// Reject naming.root in non-global (project/local) config files.
	// naming.root may only appear in the global config (~/.cf/config.toml).
	// LoadConfig's S6 check rejects it at read time; fail fast here at write time
	// to give clear feedback before the file is written.
	globalConfigPath := filepath.Join(CFHome(), "config.toml")
	if key == "naming.root" && targetPath != globalConfigPath {
		return fmt.Errorf("naming.root may only be set in the global config. Use --global flag.")
	}

	// Read existing TOML or start with empty map.
	raw := make(map[string]interface{})
	if data, err := os.ReadFile(targetPath); err == nil {
		if _, err := toml.Decode(string(data), &raw); err != nil {
			return fmt.Errorf("parsing existing config %s: %w", targetPath, err)
		}
	}

	// Set the value in the nested map structure.
	parts := strings.SplitN(key, ".", 2)
	if len(parts) != 2 {
		return fmt.Errorf("malformed key %q: expected <section>.<field>", key)
	}
	section, field := parts[0], parts[1]

	sectionMap, ok := raw[section].(map[string]interface{})
	if !ok {
		sectionMap = make(map[string]interface{})
	}

	// List fields: parse JSON array syntax.
	listFields := map[string]bool{"naming.seeds": true, "behavior.auto_join": true}
	if listFields[key] {
		var list []string
		if err := json.Unmarshal([]byte(value), &list); err != nil {
			return fmt.Errorf("key %q is a list field; value must be a JSON array (e.g. '[\"item1\",\"item2\"]'): %w", key, err)
		}
		sectionMap[field] = list
	} else if key == "behavior.walk_up" {
		switch strings.ToLower(value) {
		case "true", "1", "yes":
			sectionMap[field] = true
		case "false", "0", "no":
			sectionMap[field] = false
		default:
			return fmt.Errorf("key %q is a boolean; value must be true or false", key)
		}
	} else {
		sectionMap[field] = value
	}

	raw[section] = sectionMap

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(targetPath), 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	// Write back as TOML.
	f, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("opening config file %s: %w", targetPath, err)
	}
	defer f.Close()

	enc := toml.NewEncoder(f)
	if err := enc.Encode(raw); err != nil {
		return fmt.Errorf("writing config file %s: %w", targetPath, err)
	}

	fmt.Printf("Wrote %s\n", targetPath)
	return nil
}

// formatStringSlice formats a string slice as a compact JSON array, or "[]"
// if empty.
func formatStringSlice(s []string) string {
	if len(s) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(s)
	return string(b)
}
