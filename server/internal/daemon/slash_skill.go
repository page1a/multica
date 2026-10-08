package daemon

import (
	"fmt"
	"regexp"
	"strings"
)

var slashSkillRe = regexp.MustCompile(
	`\[/((?:[^\]\\]|\\.)+)\]\(slash://skill/([^)]+)\)`,
)

type SlashSkillRef struct {
	Label string
	ID    string
}

func ExtractSlashSkills(md string) []SlashSkillRef {
	matches := slashSkillRe.FindAllStringSubmatch(md, -1)
	seen := make(map[string]struct{}, len(matches))
	refs := make([]SlashSkillRef, 0, len(matches))

	for _, m := range matches {
		id := m[2]
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}

		label := strings.ReplaceAll(m[1], `\[`, "[")
		label = strings.ReplaceAll(label, `\]`, "]")
		refs = append(refs, SlashSkillRef{Label: label, ID: id})
	}

	return refs
}

// selectedSkillsBlock names the skills md picked through `/` skill markers,
// limited to skills bound to agent — a marker can never grant a skill the
// agent does not already have. Empty when md picks none, so a prompt pays
// nothing for it on an ordinary message. Shared by chat and comment prompts.
func selectedSkillsBlock(agent *AgentData, md string) string {
	if agent == nil || len(agent.Skills) == 0 {
		return ""
	}
	refs := ExtractSlashSkills(md)
	if len(refs) == 0 {
		return ""
	}
	agentSkills := make(map[string]string, len(agent.Skills))
	for _, s := range agent.Skills {
		agentSkills[s.ID] = s.Name
	}
	var b strings.Builder
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		name, ok := agentSkills[ref.ID]
		if !ok {
			continue
		}
		if _, dup := seen[ref.ID]; dup {
			continue
		}
		seen[ref.ID] = struct{}{}
		if b.Len() == 0 {
			b.WriteString("Explicitly selected skills:\n")
		}
		fmt.Fprintf(&b, "- %s\n", name)
	}
	if b.Len() == 0 {
		return ""
	}
	b.WriteString("\n")
	return b.String()
}
