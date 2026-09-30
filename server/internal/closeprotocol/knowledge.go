package closeprotocol

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/projectmemory"
)

// KeyKnowledgeAudit is the close-record field for what this close wrote into
// project memory. It is required on every new close. Historical records that
// predate it stay readable: it is not one of the original eight keys.
const KeyKnowledgeAudit = "close.knowledge_audit"

// KnowledgeAuditRequiredMsg is the sentence every surface shows when a close
// arrives without an audit. CLI flag checks and the HTTP handler both use it.
const KnowledgeAuditRequiredMsg = "缺知识审计：用 --knowledge-none 声明无够格知识，或用 --knowledge <位置>=<摘要> 写明改了项目记忆的哪一处"

const knowledgeSummaryMax = 500

// KnowledgeChange is one checklist slot this close wrote, plus what changed.
type KnowledgeChange struct {
	Location string `json:"location"`
	Summary  string `json:"summary"`
}

// KnowledgeAudit is either an explicit "nothing qualified" declaration or one
// or more checklist changes. The two forms cannot be combined.
type KnowledgeAudit struct {
	None    bool              `json:"none,omitempty"`
	Changes []KnowledgeChange `json:"changes,omitempty"`
}

// CanonicalKnowledgeAudit checks the audit against the project-memory
// checklist and returns the value stored on close.knowledge_audit.
func CanonicalKnowledgeAudit(audit KnowledgeAudit) (KnowledgeAudit, string, error) {
	changes := make([]KnowledgeChange, 0, len(audit.Changes))
	for _, change := range audit.Changes {
		location := strings.TrimSpace(change.Location)
		summary := strings.TrimSpace(change.Summary)
		if location == "" && summary == "" {
			continue
		}
		changes = append(changes, KnowledgeChange{Location: location, Summary: summary})
	}
	if audit.None && len(changes) > 0 {
		return KnowledgeAudit{}, "", fmt.Errorf("知识审计不能同时声明无够格知识又列出改动")
	}
	if !audit.None && len(changes) == 0 {
		return KnowledgeAudit{}, "", fmt.Errorf("%s", KnowledgeAuditRequiredMsg)
	}
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		if !projectmemory.KnownLocation(change.Location) {
			return KnowledgeAudit{}, "", fmt.Errorf("知识审计位置 %q 不在项目记忆清单里，允许的是 %s", change.Location, strings.Join(projectmemory.LocationKeys(), "、"))
		}
		if change.Summary == "" {
			return KnowledgeAudit{}, "", fmt.Errorf("知识审计位置 %q 缺改动摘要", change.Location)
		}
		if utf8.RuneCountInString(change.Summary) > knowledgeSummaryMax {
			return KnowledgeAudit{}, "", fmt.Errorf("知识审计位置 %q 的摘要超过 %d 字", change.Location, knowledgeSummaryMax)
		}
		if _, ok := seen[change.Location]; ok {
			return KnowledgeAudit{}, "", fmt.Errorf("知识审计位置 %q 写了两次", change.Location)
		}
		seen[change.Location] = struct{}{}
	}
	stored := KnowledgeAudit{}
	if audit.None {
		stored.None = true
	} else {
		stored.Changes = changes
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return KnowledgeAudit{}, "", err
	}
	return stored, string(raw), nil
}

// ParseStoredKnowledgeAudit checks a value already written on the close record.
func ParseStoredKnowledgeAudit(raw string) (KnowledgeAudit, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return KnowledgeAudit{}, fmt.Errorf("%s", KnowledgeAuditRequiredMsg)
	}
	var audit KnowledgeAudit
	if err := json.Unmarshal([]byte(raw), &audit); err != nil {
		return KnowledgeAudit{}, fmt.Errorf("%s", KnowledgeAuditRequiredMsg)
	}
	parsed, _, err := CanonicalKnowledgeAudit(audit)
	return parsed, err
}
