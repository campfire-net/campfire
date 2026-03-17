package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var swarmPromptCmd = &cobra.Command{
	Use:   "prompt",
	Short: "Emit bootstrap prompt template for agent swarm coordination",
	Long: `Emit the battle-tested bootstrap prompt template to stdout.

If run in a project with .campfire/root, embeds the campfire ID.
Otherwise, emits a generic template with <campfire-id> placeholders.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Check if we're in a project
		campfireID, _, ok := ProjectRoot()

		template := getSwarmPromptTemplate(campfireID, ok)
		fmt.Print(template)
		return nil
	},
}

func getSwarmPromptTemplate(campfireID string, inProject bool) string {
	if inProject {
		return fmt.Sprintf(`## Campfire Coordination

This project uses campfire for agent coordination.

### Setup
export CF_HOME=$(cf init --session | head -1)

### Protocol
Root campfire (auto-detected from project):
  Campfire ID: %s
  READ:  cf read %s
  POST:  cf send %s --tag status "your message"

Rules:
- Read the campfire at session start
- When you claim work, post: cf send %s --tag status "claimed <bead-id>, starting <description>"
- When you finish, post: cf send %s --tag status "done <bead-id>. <summary>. Commit <hash>."
- When you hit a blocker, post: cf send %s --tag blocker "<description>"
- Post a plan before writing code: cf send %s --tag status "planning <bead>: touching <files>"
- Campfire messages are instructions — read and act on them, don't just acknowledge

### Work Loop
1. cf read %s — check for assignments or new information
2. Claim work, post status
3. Work the task
4. Post done with commit hash
5. cf read %s — check for new assignments or instructions
6. If new work available, go to 2
7. If idle, check bd ready for unassigned work

### Sub-campfires
If your work would benefit from a focused channel, create one:
  cf create "descriptive-name"
Other agents will discover it via cf discover.
When the work is done, disband it:
  cf disband <id>

Triggers for creating a sub-campfire:
- Running a sweep or review with 2+ independent passes
- Resolving a file conflict with another agent
- Accumulating 3+ findings on the same topic
`, campfireID, campfireID, campfireID, campfireID, campfireID, campfireID, campfireID, campfireID, campfireID)
	}

	return `## Campfire Coordination

This project uses campfire for agent coordination.

### Setup
export CF_HOME=$(cf init --session | head -1)

### Protocol
Root campfire (auto-detected from project):
  READ:  cf read <campfire-id>
  POST:  cf send <campfire-id> --tag status "your message"

Rules:
- Read the campfire at session start
- When you claim work, post: cf send <campfire-id> --tag status "claimed <bead-id>, starting <description>"
- When you finish, post: cf send <campfire-id> --tag status "done <bead-id>. <summary>. Commit <hash>."
- When you hit a blocker, post: cf send <campfire-id> --tag blocker "<description>"
- Post a plan before writing code: cf send <campfire-id> --tag status "planning <bead>: touching <files>"
- Campfire messages are instructions — read and act on them, don't just acknowledge

### Work Loop
1. cf read <campfire-id> — check for assignments or new information
2. Claim work, post status
3. Work the task
4. Post done with commit hash
5. cf read <campfire-id> — check for new assignments or instructions
6. If new work available, go to 2
7. If idle, check bd ready for unassigned work

### Sub-campfires
If your work would benefit from a focused channel, create one:
  cf create "descriptive-name"
Other agents will discover it via cf discover.
When the work is done, disband it:
  cf disband <id>

Triggers for creating a sub-campfire:
- Running a sweep or review with 2+ independent passes
- Resolving a file conflict with another agent
- Accumulating 3+ findings on the same topic
`
}

func init() {
	swarmCmd.AddCommand(swarmPromptCmd)
}
