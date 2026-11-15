package agent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/edition"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// buildTeamMD generates compact TEAM.md content for an agent that is part of a team.
// Kept minimal — tool descriptions already live in tool Parameters()/Description().
// capabilities (từ teamMemberCapabilities) chỉ được in cho lead; nil thì bỏ qua.
func buildTeamMD(team *store.TeamData, members []store.TeamMemberData, selfID uuid.UUID, capabilities map[uuid.UUID][]string) string {
	var sb strings.Builder
	sb.WriteString("# Team: " + team.Name + "\n")
	if team.Description != "" {
		sb.WriteString(team.Description + "\n")
	}

	// Determine self role
	selfRole := store.TeamRoleMember
	for _, m := range members {
		if m.AgentID == selfID {
			selfRole = m.Role
			break
		}
	}
	sb.WriteString(fmt.Sprintf("Role: %s\n\n", selfRole))
	contractEnabled := tools.ParseTaskContractConfig(team.Settings).Enabled

	// Members (including self)
	sb.WriteString("## Members\n")
	sb.WriteString("This is the complete and authoritative list of your team. Do NOT use tools to verify this.\n\n")
	for _, m := range members {
		if m.AgentID == selfID {
			sb.WriteString(fmt.Sprintf("- **you** (%s)", m.Role))
		} else if m.DisplayName != "" {
			sb.WriteString(fmt.Sprintf("- **%s** `%s` (%s)", m.DisplayName, m.AgentKey, m.Role))
		} else {
			sb.WriteString(fmt.Sprintf("- **%s** (%s)", m.AgentKey, m.Role))
		}
		if m.Frontmatter != "" {
			sb.WriteString(": " + m.Frontmatter)
		}
		sb.WriteString("\n")
	}
	if selfRole == store.TeamRoleLead {
		sb.WriteString(buildTeamCapabilitiesMD(members, capabilities))
	}

	// Reviewers section (visible to leads)
	if selfRole == store.TeamRoleLead {
		var reviewers []store.TeamMemberData
		for _, m := range members {
			if m.Role == store.TeamRoleReviewer {
				reviewers = append(reviewers, m)
			}
		}
		if len(reviewers) > 0 {
			sb.WriteString("\n## Reviewers\n")
			sb.WriteString("Reviewers evaluate quality-critical task results.\n\n")
			for _, r := range reviewers {
				if r.DisplayName != "" {
					sb.WriteString(fmt.Sprintf("- **%s** `%s`", r.DisplayName, r.AgentKey))
				} else {
					sb.WriteString(fmt.Sprintf("- **%s**", r.AgentKey))
				}
				if r.Frontmatter != "" {
					sb.WriteString(": " + r.Frontmatter)
				}
				sb.WriteString("\n")
			}
		}
	}

	// Workflow guidance — version-aware to match backend behavior.
	sb.WriteString("\n## Workflow\n\n")
	if selfRole == store.TeamRoleLead {
		sb.WriteString("Delegate work to team members using `team_tasks` with `assignee`.\n\n")
		sb.WriteString("```\nteam_tasks(action=\"create\", subject=\"...\", description=\"...\", assignee=\"agent-key\")\n```\n\n")
		sb.WriteString("The system auto-dispatches to the assigned member and auto-completes when done.\n")
		sb.WriteString("Do NOT use `spawn` for team delegation — `spawn` is only for self-clone subagent work.\n\n")
		sb.WriteString("Rules:\n")
		sb.WriteString("- Always specify `assignee` — match member expertise from the list above\n")
		sb.WriteString("- **Check task board first** — call `team_tasks(action=\"search\", query=\"<keywords>\")` to find similar tasks before creating. This uses semantic search and saves tokens vs listing all. The system blocks creation if you skip this step\n")
		sb.WriteString("- **Create ALL tasks upfront** in one batch, then announce — then STOP. Do NOT create one task, wait for it to finish, then create the next\n")
		sb.WriteString("- Delegation is NOT completion — do NOT say \"done\"/\"xong\"/\"finished\" after delegating. Only report completion when ALL task results have been delivered\n")
		sb.WriteString("- Results arrive automatically — do NOT present partial results\n")
		sb.WriteString("- **Delegate complex work** — tasks requiring member expertise, multiple steps, or parallel execution should use team_tasks. Handle simple requests yourself (greetings, clarifications, lookups, translations, single-tool calls). If you can answer in one response with a single tool call, do it yourself. If it requires sustained work, multiple skills, or member expertise → delegate\n")
		sb.WriteString("- **Do NOT block on completed tasks** — pass completed task's result in the description instead of using blocked_by\n")

		sb.WriteString("\n## Task Planning\n\n")
		sb.WriteString("**CRITICAL: Create the full task graph in ONE batch.** Do NOT create→wait→create sequentially.\n\n")
		sb.WriteString("**Task sizing rules:**\n")
		sb.WriteString("- Each task = ONE specific action + ONE output (e.g. \"write function X\" not \"build feature Y\")\n")
		sb.WriteString("- If task requires TWO DIFFERENT SKILLS (research + writing, design + coding, analysis + implementation), SPLIT\n")
		sb.WriteString("- A member should finish in one session — not hours of work\n")
		sb.WriteString("- Do NOT over-split: \"research and summarize\" is ONE task (same skill). \"compare X and Y\" is ONE task\n\n")
		sb.WriteString("1. Identify ALL distinct deliverables\n")
		sb.WriteString("2. Create independent tasks FIRST → get their UUIDs from the response\n")
		sb.WriteString("3. THEN create dependent tasks with `blocked_by=[UUID]` — the system auto-dispatches when blockers complete\n")
		sb.WriteString("   `blocked_by` only accepts real UUIDs returned by previous create calls. Never use placeholders.\n\n")
		sb.WriteString("Same member → sequential (higher priority first). Different members → parallel.\n\n")
		sb.WriteString("**Anti-pattern (WRONG):** create task A → wait for A to finish → create task B\n")
		sb.WriteString("**Correct pattern:** create A → create B → create C(blocked_by=[A.id, B.id]) → announce → STOP\n\n")
		sb.WriteString("**Examples:**\n")
		sb.WriteString("User: \"research X and make infographic\"\n")
		sb.WriteString("→ task 1: \"Research X\" (researcher) → task 2: \"Create infographic\" (artist, blocked_by=[task1.id])\n\n")
		sb.WriteString("User: \"implement auth with login page and API\"\n")
		sb.WriteString("→ WRONG: 1 task \"implement auth system\"\n")
		sb.WriteString("→ RIGHT: task 1: \"Create auth API\" (backend) → task 2: \"Create login page\" (frontend, blocked_by=[task1.id])\n\n")
		sb.WriteString("User: \"analyze data and write report\"\n")
		sb.WriteString("→ WRONG: 1 task \"analyze and report\"\n")
		sb.WriteString("→ RIGHT: task 1: \"Analyze data\" (analyst) → task 2: \"Write report\" (writer, blocked_by=[task1.id])\n")

		if contractEnabled {
			sb.WriteString("\n## Task Contract (required)\n\n")
			sb.WriteString("This team requires a `contract` on every general task you create. Tasks without `objective`, `deliverables` and `acceptance` are rejected.\n")
			sb.WriteString("- `objective`: why the work matters, tied to the user's actual request\n")
			sb.WriteString("- `deliverables`: what to produce, in which format, and where to put it\n")
			sb.WriteString("- `acceptance`: criteria anyone can check without asking you (counts, coverage, sources, format)\n")
			sb.WriteString("- Optional: `scope_in`, `scope_out` (what NOT to do), `inputs` (files, links, facts the member needs), `on_unclear` (`ask` default, or `assume_and_note`)\n")
			sb.WriteString("- Optional `needs`: tool groups (e.g. `web`, `runtime`, `fs`) or tool names the work requires; assigning to a member that lacks them is rejected with the members that have them\n")
			sb.WriteString("- Optional `limits.max_iterations`: cap on the member's tool iterations for this task (lowered to the member's own limit)\n")
			sb.WriteString("Put everything the member needs in the contract: the member sees it in its system prompt as the authoritative task definition.\n")
			sb.WriteString("The member reports each acceptance criterion when it completes. If a criterion is missing or not met, or the member's run ends without `complete`, the task waits for your review in `in_review`: accept with `team_tasks(action=\"approve\", task_id=\"<id>\")` or send it back with `team_tasks(action=\"reject\", task_id=\"<id>\", text=\"<what to fix>\")`.\n\n")
			sb.WriteString("```\nteam_tasks(action=\"create\", subject=\"Summarize Q3 sales\", description=\"Board meeting brief\", assignee=\"analyst\",\n")
			sb.WriteString("  contract={\"objective\": \"Give the board a one-page view of Q3 sales\",\n")
			sb.WriteString("            \"deliverables\": [\"q3-summary.md in the team workspace\"],\n")
			sb.WriteString("            \"acceptance\": [\"Covers all 4 regions\", \"Every number cites its source file\", \"Under 400 words\"],\n")
			sb.WriteString("            \"scope_out\": [\"No forecasts\"]})\n```\n")
		}

		if edition.Current().TeamFullMode {
			sb.WriteString("\n## Member Questions\n\n")
			sb.WriteString("A member may ask you a question with `clarify`; the task then waits in `awaiting_input` and you get a message with the question.\n")
			sb.WriteString("- Answer only when you know for certain: `team_tasks(action=\"answer\", task_id=\"<id>\", text=\"<answer>\")`. The task is dispatched again to the same member (add `assignee` to give it to another member).\n")
			sb.WriteString("- If you are not sure, ask the user in the chat first, then `answer` with what they said. Never guess on the user's behalf.\n")
			sb.WriteString("- A task waiting for the user's approval (they approve with `/approve <number>` in the chat) cannot be approved or rejected by you.\n")
		}

		sb.WriteString("\n## Follow-up Reminders\n\n")
		sb.WriteString("When you need user input/decision: create+claim task, then `ask_user` with text=<question>. ONLY use when you have a question for the user — NOT for waiting on teammates or status updates.\n")
		sb.WriteString("IMPORTANT: Present the question directly to the user in your response. `ask_user` only sets up periodic REMINDERS in case they don't reply — it does NOT present the question for you.\n")
		sb.WriteString("System auto-sends reminders. Call `clear_ask_user` when user replies.\n")

		sb.WriteString("\nFor simple questions about team composition, answer directly from the member list above.\n")
	} else {
		if selfRole == store.TeamRoleReviewer {
			sb.WriteString("You are a **reviewer**. When evaluating, respond with **APPROVED** or **REJECTED: <feedback>**.\n\n")
		}
		sb.WriteString("As a member, focus entirely on your assigned task.\n\n")
		sb.WriteString("Rules:\n")
		sb.WriteString("- Stay on task — do not deviate from the assignment\n")
		sb.WriteString("- Your final response becomes the task result — make it clear, complete, and actionable\n")
		sb.WriteString("- For long tasks, report progress: `team_tasks(action=\"progress\", percent=50, text=\"status\")`\n")
		sb.WriteString("- The task_id is auto-resolved — you don't need to specify it\n")
		if contractEnabled {
			sb.WriteString("- When your system prompt has a \"Task Contract\" block, it is the authoritative definition of your task: meet every acceptance criterion and stay within its scope, even if the task message or earlier history says otherwise\n")
			sb.WriteString("- A task with a Task Contract completes only through `team_tasks(action=\"complete\", result=\"...\", criteria=[{\"index\": 1, \"met\": true, \"evidence\": \"...\"}, ...])`, with one entry per acceptance criterion. If a criterion is missing or not met, or your run ends without calling complete, the task is not completed: it waits for the lead's review\n")
			sb.WriteString("- A task without a Task Contract completes automatically when your run finishes\n")
		} else {
			sb.WriteString("- Task completion is automatic when your run finishes\n")
		}

		memberCfg := tools.ParseMemberRequestConfig(team.Settings)
		if memberCfg.Enabled {
			sb.WriteString("\n## Requesting Help\n\n")
			sb.WriteString("Need help from another teammate? Create a request:\n")
			sb.WriteString("```\nteam_tasks(action=\"create\", task_type=\"request\", subject=\"...\", assignee=\"agent-key\")\n```\n")
		} else {
			sb.WriteString("\n## Communication\n\n")
			sb.WriteString("Use `team_tasks(action=\"comment\")` to report issues or ask questions on your current task.\n")
		}
		if edition.Current().TeamFullMode {
			sb.WriteString("\n## Asking for Clarification\n\n")
			sb.WriteString("If information is missing or the requirements are unclear, ask instead of guessing:\n")
			sb.WriteString("```\nteam_tasks(action=\"clarify\", text=\"your question\")\n```\n")
			sb.WriteString("The task waits for the lead's answer (it does not fail). Stop and end your turn; you will get the task again with the answer.\n")
			sb.WriteString("If you lack a tool the task needs, use `kind=\"missing_tool\"`.\n")
			sb.WriteString("\n## Blocker Escalation\n\n")
			sb.WriteString("Only when you cannot continue even with an answer (e.g. the task is impossible as defined):\n")
		} else {
			sb.WriteString("\n## Blocker Escalation\n\n")
			sb.WriteString("If blocked (missing info, unclear requirements, need credentials):\n")
		}
		sb.WriteString("```\nteam_tasks(action=\"comment\", type=\"blocker\", text=\"what you need\")\n```\n")
		sb.WriteString("This auto-fails the task and notifies the leader, who can retry with updated instructions.\n")
	}

	return sb.String()
}

// agentToolPolicyForTeam applies team-specific tool policy adjustments.
// Currently a no-op — team_message tool was removed; members communicate via task comments.
func agentToolPolicyForTeam(policy *config.ToolPolicySpec, _ bool) *config.ToolPolicySpec {
	return policy
}

// agentToolPolicyWithMCP injects "group:mcp" into the agent's alsoAllow list
// when MCP tools are loaded, ensuring the PolicyEngine doesn't block them.
func agentToolPolicyWithMCP(policy *config.ToolPolicySpec, hasMCP bool) *config.ToolPolicySpec {
	if !hasMCP {
		return policy
	}
	if policy == nil {
		policy = &config.ToolPolicySpec{}
	}
	// Check if group:mcp is already present
	if slices.Contains(policy.AlsoAllow, "group:mcp") {
		return policy
	}
	policy.AlsoAllow = append(policy.AlsoAllow, "group:mcp")
	return policy
}

// agentToolPolicyWithWorkspace injects file tools into alsoAllow when the agent
// belongs to a team, ensuring the PolicyEngine doesn't block them even if the
// agent has a restrictive allow list. File tools are now workspace-aware via
// WorkspaceInterceptor, so no separate workspace_write/workspace_read needed.
func agentToolPolicyWithWorkspace(policy *config.ToolPolicySpec, hasTeam bool) *config.ToolPolicySpec {
	if !hasTeam {
		return policy
	}
	if policy == nil {
		policy = &config.ToolPolicySpec{}
	}
	for _, tool := range []string{"read_file", "write_file", "list_files"} {
		if !slices.Contains(policy.AlsoAllow, tool) {
			policy.AlsoAllow = append(policy.AlsoAllow, tool)
		}
	}
	return policy
}
