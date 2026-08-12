package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var frontmatterRe = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n`)

type SkillsTool struct {
	workspace   string
	internalDir string
	externalDir string
}

func NewSkillsTool(workspace string) *SkillsTool {
	return &SkillsTool{
		workspace:   workspace,
		internalDir: filepath.Join(workspace, "skills"),
		externalDir: filepath.Join(workspace, ".apiguide", "skills"),
	}
}

func (t *SkillsTool) Name() string { return "skills" }
func (t *SkillsTool) Description() string {
	return "List available skills (internal and external). Use action='list' or action='read'."
}
func (t *SkillsTool) IsHidden() bool { return false }
func (t *SkillsTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action":     map[string]interface{}{"type": "string", "description": "Action: 'list' or 'read'", "enum": []string{"list", "read"}},
			"source":     map[string]interface{}{"type": "string", "description": "Filter: 'internal' or 'external'", "enum": []string{"internal", "external"}},
			"skill_name": map[string]interface{}{"type": "string", "description": "Skill name to read"},
		},
		"required": []string{},
	}
}

func (t *SkillsTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	action, _ := params["action"].(string)
	if action == "" {
		action = "list"
	}
	if action == "read" {
		return t.readSkill(params)
	}
	return t.listSkills(params)
}

func (t *SkillsTool) listSkills(params map[string]interface{}) (*ToolResult, error) {
	source, _ := params["source"].(string)
	skills := t.scanSkills()
	if source != "" {
		var filtered []map[string]interface{}
		for _, s := range skills {
			if s["source"] == source {
				filtered = append(filtered, s)
			}
		}
		skills = filtered
	}
	if len(skills) == 0 {
		return NewTextResult("No skills found."), nil
	}
	lines := []string{"Available skills:\n"}
	for _, s := range skills {
		tag := "[internal]"
		if s["source"] == "external" {
			tag = "[external]"
		}
		lines = append(lines, fmt.Sprintf("  %s %s", tag, s["name"]))
		if desc, ok := s["description"].(string); ok && desc != "" {
			lines = append(lines, fmt.Sprintf("    Description: %s", desc))
		}
		lines = append(lines, fmt.Sprintf("    Path: %s", s["path"]))
		lines = append(lines, "")
	}
	lines = append(lines, fmt.Sprintf("Total: %d skills", len(skills)))
	return NewTextResult(strings.Join(lines, "\n")), nil
}

func (t *SkillsTool) readSkill(params map[string]interface{}) (*ToolResult, error) {
	skillName, _ := params["skill_name"].(string)
	if skillName == "" {
		return NewTextResult("Error: provide skill_name parameter"), nil
	}
	skills := t.scanSkills()
	for _, s := range skills {
		if s["name"] == skillName {
			parts := []string{
				fmt.Sprintf("Skill: %s", s["name"]),
				fmt.Sprintf("Source: %s", s["source"]),
				fmt.Sprintf("Path: %s", s["path"]),
			}
			if desc, ok := s["description"].(string); ok && desc != "" {
				parts = append(parts, fmt.Sprintf("Description: %s", desc))
			}
			parts = append(parts, fmt.Sprintf("\n--- Content ---\n%s", s["body"]))
			return NewTextResult(strings.Join(parts, "\n")), nil
		}
	}
	var names []string
	for _, s := range skills {
		names = append(names, s["name"].(string))
	}
	return NewTextResult(fmt.Sprintf("Error: skill '%s' not found. Available: %s", skillName, strings.Join(names, ", "))), nil
}

func (t *SkillsTool) scanSkills() []map[string]interface{} {
	var skills []map[string]interface{}
	for _, dir := range []struct{ path, label string }{
		{t.internalDir, "internal"},
		{t.externalDir, "external"},
	} {
		entries, err := os.ReadDir(dir.path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				subEntries, _ := os.ReadDir(filepath.Join(dir.path, e.Name()))
				for _, se := range subEntries {
					if se.Name() == "SKILL.md" {
						skillPath := filepath.Join(dir.path, e.Name(), "SKILL.md")
						if info := t.parseSkillFile(skillPath); info != nil {
							info["source"] = dir.label
							skills = append(skills, info)
						}
					}
				}
			}
		}
	}
	return skills
}

func (t *SkillsTool) parseSkillFile(path string) map[string]interface{} {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	content := string(data)
	meta, body := parseFrontmatter(content)
	name, _ := meta["name"].(string)
	if name == "" {
		name = filepath.Base(filepath.Dir(path))
	}
	return map[string]interface{}{
		"name":        name,
		"description": meta["description"],
		"path":        path,
		"body":        body,
	}
}

func parseFrontmatter(content string) (map[string]interface{}, string) {
	match := frontmatterRe.FindStringSubmatch(content)
	if match == nil {
		return nil, content
	}
	fmText := match[1]
	body := content[len(match[0]):]
	meta := make(map[string]interface{})
	var currentKey string
	var currentSub map[string]interface{}

	for _, line := range strings.Split(fmText, "\n") {
		stripped := strings.TrimSpace(line)
		if stripped == "" {
			continue
		}
		if strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
			if currentKey != "" && currentSub != nil {
				if parts := strings.SplitN(stripped, ":", 2); len(parts) == 2 {
					currentSub[strings.TrimSpace(parts[0])] = coerce(strings.TrimSpace(parts[1]))
				}
			}
			continue
		}
		if parts := strings.SplitN(stripped, ":", 2); len(parts) == 2 {
			currentKey = strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			if val != "" {
				meta[currentKey] = coerce(val)
				currentSub = nil
			} else {
				currentSub = make(map[string]interface{})
				meta[currentKey] = currentSub
			}
		}
	}
	return meta, strings.TrimSpace(body)
}

func coerce(val string) interface{} {
	lower := strings.ToLower(val)
	if lower == "true" || lower == "yes" {
		return true
	}
	if lower == "false" || lower == "no" {
		return false
	}
	if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
		return val[1 : len(val)-1]
	}
	return val
}
