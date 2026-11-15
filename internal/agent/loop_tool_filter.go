package agent

import (
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// imageGenToolDef is the native image_generation tool sentinel. The request
// builder keys off Type (Codex emits a bare {"type":"image_generation"} object,
// no function wrapper). Function is populated with a name-only schema so the many
// pipeline/provider sites that read td.Function.Name (think_stage allowlist,
// shouldRetryTaskMCP, history tool names, non-codex request builders) never
// nil-deref — the v3.14.0 crash was one such site.
var imageGenToolDef = providers.ToolDefinition{
	Type:     "image_generation",
	Function: &providers.ToolFunctionSchema{Name: "image_generation"},
}

func (l *Loop) toolVisibleForChannel(name, channelType string, telegramManagerPermissions []string) bool {
	return tools.ToolVisibleForChannel(l.tools, name, channelType, telegramManagerPermissions, false)
}

// buildFilteredTools resolves the per-iteration tool definitions based on policy,
// disabled tools, bootstrap mode, skill visibility, channel type, and iteration budget.
// Per-user MCP tools (require_user_credentials servers) are passed in via userTools —
// their objects deliberately live ONLY in l.mcpUserTools (cross-user isolation), NOT
// in the shared registry. They are surfaced via a request-scoped overlay registry so
// the PolicyEngine evaluates AND emits them under the SAME allow/deny rules as
// registry tools; execution routes per-actor via executeToolForActor.
// Returns tool definitions for the provider, an allowed-tools map for execution validation,
// and the (potentially modified) messages slice when final-iteration stripping appends a hint.
func (l *Loop) buildFilteredTools(req *RunRequest, hadBootstrap bool, iteration, maxIter int, messages []providers.Message, userTools []tools.Tool) ([]providers.ToolDefinition, map[string]bool, []providers.Message) {
	// Build provider request with policy-filtered tools.
	var toolDefs []providers.ToolDefinition
	var allowedTools map[string]bool
	if l.toolPolicy != nil {
		// Per-user MCP tool objects are NOT in the shared registry (cross-user
		// isolation — see getUserMCPTools), so FilterTools alone cannot emit them.
		// Wrap the registry in a request-scoped overlay that adds the calling actor's
		// per-user tools: FilterTools then evaluates them through the same policy
		// pipeline (profile/allow/deny, incl. an explicit deny on a per-user tool name)
		// and emits them via overlay.Get. The overlay is local and discarded after this
		// call, so the shared registry — and thus other users — stay unaffected.
		// NewUserToolOverlay returns l.tools unchanged when userTools is empty.
		registry := tools.NewUserToolOverlay(l.tools, userTools)
		toolDefs = l.toolPolicy.FilterTools(registry, l.id, l.provider.Name(), l.agentToolPolicy, req.ToolAllow, false, false)
		allowedTools = make(map[string]bool, len(toolDefs))
		for _, td := range toolDefs {
			if td.Function != nil {
				allowedTools[td.Function.Name] = true
			}
		}
	} else {
		// No policy → all tools allowed. ProviderDefs() omits per-user MCP tools (not in
		// the shared registry), so append their defs directly (dedup by name).
		toolDefs = l.tools.ProviderDefs()
		if len(userTools) > 0 {
			seen := make(map[string]bool, len(toolDefs))
			for _, td := range toolDefs {
				if td.Function != nil {
					seen[td.Function.Name] = true
				}
			}
			for _, t := range userTools {
				if t == nil || seen[t.Name()] {
					continue
				}
				seen[t.Name()] = true
				toolDefs = append(toolDefs, tools.ToProviderDef(t))
			}
		}
	}

	// Post-policy filter chain (orchestration mode, tenant exclusions, bootstrap,
	// skill_evolve, channel). Shared with team task needs checks via
	// tools.RunToolFilter so assignment-time capability matches run time.
	//   - orchestration mode: spawn hides delegate + team_tasks; delegate hides team_tasks.
	//   - tenant exclusions: tools disabled for this agent's tenant.
	//   - bootstrap (open agents only): restrict to write_file; predefined agents keep all tools.
	//   - skill_manage stays in the shared registry but is hidden when skill_evolve is off.
	//   - channel-aware tools are hidden when the channel type doesn't match (or is empty);
	//     telegram_manager also requires explicit channel permissions.
	var bootstrapAllow map[string]bool
	if hadBootstrap && l.agentType != store.AgentTypePredefined {
		bootstrapAllow = bootstrapToolAllowlist
	}
	toolDefs = tools.RunToolFilter{
		DenyNames:       orchModeDenyTools(l.orchMode),
		Disabled:        l.disabledTools,
		BootstrapAllow:  bootstrapAllow,
		HideSkillManage: !l.skillEvolve,
		Channel: &tools.ChannelToolFilter{
			Registry:                   l.tools,
			Type:                       req.ChannelType,
			TelegramManagerPermissions: req.TelegramManagerPermissions,
		},
	}.Apply(toolDefs, allowedTools)

	// Final iteration: strip all tools to force a text-only response.
	// Without this the model may keep requesting tools and exit with "...".
	if iteration == maxIter {
		toolDefs = nil
		messages = append(messages, providers.Message{
			Role:    "user",
			Content: "[System] Final iteration reached. Summarize all findings and respond to the user now. No more tool calls allowed.",
		})
		return toolDefs, allowedTools, messages
	}

	// Two-tier image generation gate:
	//   (1) provider supports native image_generation (ImageGeneration capability)
	//   (2) agent config allows it (allowImageGeneration — defaults true, set false via
	//       other_config.allow_image_generation = false in the admin agent configuration)
	if l.allowImageGeneration {
		if aware, ok := l.provider.(providers.CapabilitiesAware); ok {
			if aware.Capabilities().ImageGeneration {
				toolDefs = append(toolDefs, imageGenToolDef)
			}
		}
	}

	return toolDefs, allowedTools, messages
}
