package cmd

// assignCommandGroups maps each subcommand to its help group.
// Called from Execute() after all init() functions have registered commands,
// ensuring group assignments are in place before cobra renders help output.
func assignCommandGroups() {
	groups := map[string]string{
		// Convention Operations — working with typed conventions and swarms.
		"convention": groupConventions,
		"swarm":      groupConventions,
		"discover":   groupConventions,

		// Campfire Management — identity, membership, lifecycle.
		"init":     groupCampfire,
		"identity": groupCampfire,
		"id":       groupCampfire,
		"join":     groupCampfire,
		"leave":    groupCampfire,
		"create":   groupCampfire,
		"disband":  groupCampfire,
		"admit":    groupCampfire,
		"evict":    groupCampfire,
		"home":     groupCampfire,
		"member":   groupCampfire,
		"members":  groupCampfire,
		"alias":    groupCampfire,
		"name":     groupCampfire,
		"root":        groupCampfire,
		"join-policy": groupCampfire,
		"trust":        groupCampfire,
		"ls":       groupCampfire,

		// Messages — reading, writing, and querying messages.
		"send":       groupMessages,
		"read":       groupMessages,
		"await":      groupMessages,
		"compact":    groupMessages,
		"dm":         groupMessages,
		"view":       groupMessages,
		"inspect":    groupMessages,
		"provenance": groupMessages,
		"dag":        groupMessages,

		// Advanced — low-level primitives, bridges, server, and tooling.
		"bridge":     groupAdvanced,
		"serve":      groupAdvanced,
		"verify":     groupAdvanced,
		"seed":       groupAdvanced,
		"filter":     groupAdvanced,
		"sync":       groupAdvanced,
		"nat-poll":   groupAdvanced,
		"completion": groupAdvanced,
		"help":       groupAdvanced,
	}

	for _, sub := range rootCmd.Commands() {
		if gid, ok := groups[sub.Name()]; ok {
			sub.GroupID = gid
		}
	}
}
