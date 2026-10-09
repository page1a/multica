package closeprotocol

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/internal/projectmemory"
)

// Memory hygiene (DENE-1680): project memory only grows in order. A boss-layer
// sediment — the round a parent ticket or a project opens, or a chat that
// settles its work — says for each change whether it adds, updates, waits to
// merge into, or supersedes an entry; an old rule is marked 「已被 X 取代」
// instead of quietly deleted; and a map file over its size limit refuses the
// delivery that touches it until entries are merged or dropped. The CLI reads
// the file facts from git (MemoryFile); the server judges them here.

// The change actions a knowledge audit may declare.
const (
	ActionNew       = "new"       // a new entry; nothing related existed
	ActionUpdate    = "update"    // rewrote an existing entry in place
	ActionMerge     = "merge"     // new entry, marked to merge into Entry later
	ActionSupersede = "supersede" // Entry is marked 「已被 X 取代」, not deleted
)

// MapFileMaxBytes caps each map file (the checklist's file slots: AGENTS.md,
// CONTEXT.md, the docs and evidence indexes). It is Codex's project-doc
// budget: past it an agent's context carries the map but not the work.
const MapFileMaxBytes = 32 * 1024

// MemoryFile is what git says about one delivered project-memory file: its
// size after the delivery, the existing lines the delivery removed or
// rewrote (a git diff's "-" lines), how many
// added lines carry a supersede mark, and its sections (only needed to name
// what to merge when it is over the limit).
type MemoryFile struct {
	Path           string          `json:"path"`
	Bytes          int             `json:"bytes"`
	Deleted        int             `json:"deleted"`
	SupersedeMarks int             `json:"supersede_marks"`
	Sections       []MemorySection `json:"sections,omitempty"`
}

// MemorySection is one heading of a map file and the bytes under it.
type MemorySection struct {
	Heading    string `json:"heading"`
	Bytes      int    `json:"bytes"`
	Superseded bool   `json:"superseded,omitempty"`
}

var supersedeMark = regexp.MustCompile(`已被.{0,120}取代|(?i)superseded by`)

// IsSupersedeMark reports whether a line marks an entry as replaced.
func IsSupersedeMark(line string) bool { return supersedeMark.MatchString(line) }

var mdHeading = regexp.MustCompile(`^#{1,4}\s+(.+?)\s*#*\s*$`)

// MemorySections splits a markdown map file into its headings with the bytes
// each one holds up to the next heading. Text before the first heading is
// left out: it is the file's preamble, not an entry.
func MemorySections(content string) []MemorySection {
	var out []MemorySection
	inFence := false
	for _, line := range strings.SplitAfter(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
		}
		if !inFence {
			if m := mdHeading.FindStringSubmatch(strings.TrimRight(line, "\r\n")); m != nil {
				out = append(out, MemorySection{Heading: m[1]})
			}
		}
		if len(out) == 0 {
			continue
		}
		last := &out[len(out)-1]
		last.Bytes += len(line)
		if IsSupersedeMark(line) {
			last.Superseded = true
		}
	}
	return out
}

// checkChangeAction is the per-change part of CanonicalKnowledgeAudit.
func checkChangeAction(change KnowledgeChange) error {
	switch change.Action {
	case "", ActionNew:
		return nil
	case ActionUpdate, ActionMerge, ActionSupersede:
		if change.Entry == "" {
			return fmt.Errorf("知识审计位置 %q 声明了 %s，要写明是哪个已有条目：--knowledge %s:%s:<条目>=<摘要>", change.Location, change.Action, change.Location, change.Action)
		}
		return nil
	default:
		return fmt.Errorf("知识审计位置 %q 的动作 %q 不认识，允许的是 new、update、merge、supersede", change.Location, change.Action)
	}
}

// BossLayerHint is the one line a boss-layer round and a refusal show about
// how to declare changes.
const BossLayerHint = "写入前先在地图文件里找已有条目，每处改动声明动作：--knowledge <位置>:update:<条目>=<摘要>（改原条目）、<位置>:merge:<条目>=…（新建并标待合并）、<位置>:supersede:<条目>=<被谁取代>（原条目标「已被 X 取代」，不删）、<位置>:new=…（确实没有相关条目）"

// CheckMemoryHygiene holds an audit and the delivery's memory files to the
// hygiene rules. boss is a boss-layer sediment. files nil means the caller
// sent no facts (the web, an older CLI): only the declaration rules apply.
func CheckMemoryHygiene(audit KnowledgeAudit, files *[]MemoryFile, boss bool) error {
	if boss && !audit.None {
		var missing []string
		for _, change := range audit.Changes {
			if change.Action == "" {
				missing = append(missing, change.Location)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("这是老板层沉淀，%s 没声明动作。%s", strings.Join(missing, "、"), BossLayerHint)
		}
	}
	if files == nil {
		return nil
	}
	if msg := mapFileOverLimit(*files); msg != "" {
		return fmt.Errorf("%s", msg)
	}
	if audit.None {
		if boss {
			for _, file := range *files {
				if file.Deleted > 0 {
					return fmt.Errorf("%s 改掉或删掉了 %d 行已有内容，老板层沉淀不能用 --knowledge-none 带过：改原条目要声明 update，过时规则标「已被 X 取代」并声明 supersede。%s", file.Path, file.Deleted, BossLayerHint)
				}
			}
		}
		return nil
	}
	byLocation := map[string][]MemoryFile{}
	for _, file := range *files {
		for _, location := range projectmemory.Locations() {
			if len(projectmemory.MatchFiles(location.Key, []string{file.Path})) > 0 {
				byLocation[location.Key] = append(byLocation[location.Key], file)
			}
		}
	}
	declared := map[string]map[string]bool{}
	for _, change := range audit.Changes {
		if declared[change.Location] == nil {
			declared[change.Location] = map[string]bool{}
		}
		declared[change.Location][change.Action] = true
	}
	for _, change := range audit.Changes {
		if change.Action != ActionSupersede {
			continue
		}
		marks := 0
		for _, file := range byLocation[change.Location] {
			marks += file.SupersedeMarks
		}
		if marks == 0 {
			return fmt.Errorf("知识审计说「%s」已被取代，但本次交付没在 %s 里留下「已被 X 取代」的标注：在原条目旁写上它被谁取代，再重新收口", change.Entry, locationPath(change.Location))
		}
	}
	if !boss {
		return nil
	}
	for _, location := range projectmemory.Locations() {
		acts := declared[location.Key]
		if acts[ActionUpdate] || acts[ActionSupersede] {
			continue
		}
		for _, file := range byLocation[location.Key] {
			if file.Deleted > 0 {
				return fmt.Errorf("%s 改掉或删掉了 %d 行已有内容，但知识审计没声明 update 或 supersede：改原条目要声明 update，过时规则标「已被 X 取代」并声明 supersede，不悄悄删。%s", file.Path, file.Deleted, BossLayerHint)
			}
		}
	}
	return nil
}

// mapFileOverLimit names the first map file over MapFileMaxBytes and what to
// trim: entries already marked superseded first, then the largest sections
// until the overflow is covered.
func mapFileOverLimit(files []MemoryFile) string {
	for _, file := range files {
		if file.Bytes <= MapFileMaxBytes || !isMapFile(file.Path) {
			continue
		}
		over := file.Bytes - MapFileMaxBytes
		var stale, rest []MemorySection
		for _, section := range file.Sections {
			if section.Superseded {
				stale = append(stale, section)
			} else {
				rest = append(rest, section)
			}
		}
		sort.SliceStable(rest, func(i, j int) bool { return rest[i].Bytes > rest[j].Bytes })
		var parts []string
		covered := 0
		if len(stale) > 0 {
			names := make([]string, 0, len(stale))
			for _, section := range stale {
				names = append(names, "「"+section.Heading+"」")
				covered += section.Bytes
			}
			parts = append(parts, "先删已标取代的 "+strings.Join(names, ""))
		}
		var largest []string
		for _, section := range rest {
			if covered >= over && len(largest) > 0 {
				break
			}
			largest = append(largest, fmt.Sprintf("「%s」(%d 字节)", section.Heading, section.Bytes))
			covered += section.Bytes
			if len(largest) == 3 {
				break
			}
		}
		if len(largest) > 0 {
			parts = append(parts, "合并或精简最大的 "+strings.Join(largest, "、"))
		}
		advice := "合并或删掉旧条目"
		if len(parts) > 0 {
			advice = strings.Join(parts, "，再")
		}
		return fmt.Sprintf("%s 有 %d 字节，超过地图文件上限 %d（多 %d）：%s，降到上限内再重新收口", file.Path, file.Bytes, MapFileMaxBytes, over, advice)
	}
	return ""
}

func isMapFile(path string) bool {
	for _, location := range projectmemory.Locations() {
		if location.Kind == "file" && len(projectmemory.MatchFiles(location.Key, []string{path})) > 0 {
			return true
		}
	}
	return false
}
