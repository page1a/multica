// Package stagegate decides which next-stage sub-issues the platform may
// promote on its own when a stage barrier closes.
//
// A description that states no extra dependency is promotable: the previous
// stage being done is the only dependency, and the barrier already checked
// that. A description that names a dependency we can see is done is also
// promotable. Anything we cannot check, or that conflicts with the stage
// layout, stays in backlog for a person.
package stagegate

import (
	"regexp"
	"strings"
)

// Item is one sibling under the parent, in its canonical status.
type Item struct {
	ID          string
	Title       string
	Description string
	Identifier  string
	Stage       int32
	HasStage    bool
	Status      string
}

// Hold is a next-stage backlog item the platform will not promote.
type Hold struct {
	ID         string
	Identifier string
	Title      string
	Reason     string
}

var (
	noExtraDep = regexp.MustCompile(`(?i)(没有额外依赖|无额外依赖|no extra depend)`)
	conflictRe = regexp.MustCompile(`(?i)(不要推进|先确认|与.{0,40}冲突|描述冲突|依赖不明|do not promote|leave it backlog)`)
	depRe      = regexp.MustCompile(`(?i)(依赖|depends on|blocked by|前置|等.{0,40}完成|after\s+[A-Za-z][A-Za-z0-9]*-\d+)`)
	identRe    = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]*-\d+`)
	stageRe    = regexp.MustCompile(`(?i)(?:stage|阶段)\s*(\d+)`)
)

// Classify splits the next stage's backlog items into ones the platform may
// promote and ones it must leave. Items that are not in that stage, or not
// in backlog, are ignored.
func Classify(nextStage int32, items []Item) (promote []Item, hold []Hold) {
	if nextStage <= 0 {
		return nil, nil
	}
	byIdent := map[string]Item{}
	for _, item := range items {
		if item.Identifier != "" {
			byIdent[strings.ToUpper(item.Identifier)] = item
		}
	}
	for _, item := range items {
		if !item.HasStage || item.Stage != nextStage || item.Status != "backlog" {
			continue
		}
		if reason, ok := unclear(item, nextStage, items, byIdent); ok {
			hold = append(hold, Hold{
				ID: item.ID, Identifier: item.Identifier, Title: item.Title, Reason: reason,
			})
			continue
		}
		promote = append(promote, item)
	}
	return promote, hold
}

func unclear(item Item, nextStage int32, items []Item, byIdent map[string]Item) (string, bool) {
	text := strings.TrimSpace(item.Description)
	if text == "" {
		return "", false
	}
	if conflictRe.MatchString(text) {
		return "描述和阶段安排冲突，或写明要先确认", true
	}
	if noExtraDep.MatchString(text) && !depRe.MatchString(stripNoExtra(text)) {
		return "", false
	}
	if !depRe.MatchString(text) {
		if other, ok := titleDependency(item, items); ok {
			return "描述点名了还没完成的「" + other + "」", true
		}
		return "", false
	}
	idents := identRe.FindAllString(text, -1)
	stages := stageRe.FindAllStringSubmatch(text, -1)
	if len(idents) == 0 && len(stages) == 0 {
		return "写了依赖，但没有指到具体的票或阶段", true
	}
	for _, raw := range idents {
		key := strings.ToUpper(raw)
		if item.Identifier != "" && key == strings.ToUpper(item.Identifier) {
			continue
		}
		other, ok := byIdent[key]
		if !ok {
			return "依赖 " + raw + "，这张父票的子票里对不上", true
		}
		if !terminal(other.Status) {
			label := other.Identifier
			if label == "" {
				label = other.Title
			}
			return "依赖的 " + label + " 还没完成", true
		}
	}
	for _, match := range stages {
		n := atoi(match[1])
		if n <= 0 {
			return "写了阶段依赖，但阶段号读不出来", true
		}
		if n >= int(nextStage) {
			return "依赖的阶段不在已经完成的阶段里", true
		}
		if !stageTerminal(int32(n), items) {
			return "依赖的阶段还有没完成的子票", true
		}
	}
	if other, ok := titleDependency(item, items); ok {
		return "描述点名了还没完成的「" + other + "」", true
	}
	return "", false
}

func stripNoExtra(text string) string {
	return noExtraDep.ReplaceAllString(text, "")
}

func titleDependency(item Item, items []Item) (string, bool) {
	text := item.Description
	for _, other := range items {
		if other.ID == item.ID || terminal(other.Status) {
			continue
		}
		title := strings.TrimSpace(other.Title)
		if len([]rune(title)) < 8 {
			continue
		}
		if strings.Contains(text, title) {
			return title, true
		}
	}
	return "", false
}

func stageTerminal(stage int32, items []Item) bool {
	seen := false
	for _, item := range items {
		if !item.HasStage || item.Stage != stage {
			continue
		}
		seen = true
		if !terminal(item.Status) {
			return false
		}
	}
	return seen
}

func terminal(status string) bool {
	return status == "done" || status == "cancelled"
}

func atoi(raw string) int {
	n := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
