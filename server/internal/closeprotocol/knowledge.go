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
// Files are the delivered paths that write the slot (DENE-1661): the server
// fills them from the delivery's file list, never from the caller's claim.
//
// Action and Entry say how the change treats what the file already holds
// (DENE-1680): a new entry, an update of an existing one, a new one waiting to
// be merged into an existing one, or an old one marked superseded. A boss-layer
// sediment must say it for every change; elsewhere it is optional.
type KnowledgeChange struct {
	Location string   `json:"location"`
	Action   string   `json:"action,omitempty"`
	Entry    string   `json:"entry,omitempty"`
	Summary  string   `json:"summary"`
	Files    []string `json:"files,omitempty"`
}

// KnowledgeAudit is either an explicit "nothing qualified" declaration or one
// or more checklist changes. The two forms cannot be combined. Unverified
// marks changes nobody could hold against a delivery: the caller sent no file
// list (a person closing from the web, or a CLI that predates the check).
type KnowledgeAudit struct {
	None       bool              `json:"none,omitempty"`
	Changes    []KnowledgeChange `json:"changes,omitempty"`
	Unverified bool              `json:"unverified,omitempty"`
}

// KnowledgeUnverifiedWarning is the close's warning for an audit whose
// changes went unchecked because no delivered file list came with it.
const KnowledgeUnverifiedWarning = "知识审计写了改动，但这次收口没带交付文件清单，没能核对沉淀是否随交付进主线；在任务工作目录里用新版 `multica issue close` 收口才会核对"

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
		changes = append(changes, KnowledgeChange{
			Location: location, Action: strings.ToLower(strings.TrimSpace(change.Action)), Entry: strings.TrimSpace(change.Entry),
			Summary: summary, Files: cleanFiles(change.Files),
		})
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
		if err := checkChangeAction(change); err != nil {
			return KnowledgeAudit{}, "", err
		}
		// One location may carry several changes when each names its own
		// entry (update one rule, supersede another); the same entry twice
		// is still a typo.
		key := change.Location + "\x00" + change.Entry
		if _, ok := seen[key]; ok {
			if change.Entry == "" {
				return KnowledgeAudit{}, "", fmt.Errorf("知识审计位置 %q 写了两次", change.Location)
			}
			return KnowledgeAudit{}, "", fmt.Errorf("知识审计位置 %q 的条目「%s」写了两次", change.Location, change.Entry)
		}
		seen[key] = struct{}{}
	}
	stored := KnowledgeAudit{}
	if audit.None {
		stored.None = true
	} else {
		stored.Changes = changes
		stored.Unverified = audit.Unverified
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

// StripKnowledgeEvidence drops what only the server may write: the files bound
// to each change and the unverified mark. A request's audit goes through this
// before BindDeliveredFiles, so a caller cannot claim its own proof.
func StripKnowledgeEvidence(audit KnowledgeAudit) KnowledgeAudit {
	out := KnowledgeAudit{None: audit.None}
	for _, change := range audit.Changes {
		out.Changes = append(out.Changes, KnowledgeChange{Location: change.Location, Action: change.Action, Entry: change.Entry, Summary: change.Summary})
	}
	return out
}

// BindDeliveredFiles holds a canonical audit against the paths this delivery
// changes (DENE-1661). Every listed location must be written by at least one
// delivered file, and those files are recorded on the change. delivered nil
// means no list came with the close: the audit is kept and marked Unverified.
// "无够格知识" needs no files and passes either way.
func BindDeliveredFiles(audit KnowledgeAudit, delivered *[]string) (KnowledgeAudit, error) {
	if audit.None || len(audit.Changes) == 0 {
		return audit, nil
	}
	if delivered == nil {
		audit.Unverified = true
		return audit, nil
	}
	out := KnowledgeAudit{Changes: make([]KnowledgeChange, 0, len(audit.Changes))}
	var missing []string
	for _, change := range audit.Changes {
		files := projectmemory.MatchFiles(change.Location, *delivered)
		if len(files) == 0 {
			missing = append(missing, change.Location+"（"+locationPath(change.Location)+"）")
		}
		change.Files = files
		out.Changes = append(out.Changes, change)
	}
	if len(missing) > 0 {
		return KnowledgeAudit{}, fmt.Errorf("知识审计写了 %s，但本次交付的改动里没有对应文件：沉淀要提交进本次交付的分支（PR 或父票分支）随它合入主线，再重新 close；确实没写就改用 --knowledge-none", strings.Join(missing, "、"))
	}
	return out, nil
}

func locationPath(key string) string {
	for _, location := range projectmemory.Locations() {
		if location.Key == key {
			return location.Path
		}
	}
	return key
}

func cleanFiles(files []string) []string {
	var out []string
	for _, file := range files {
		if file = strings.TrimSpace(file); file != "" {
			out = append(out, file)
		}
	}
	return out
}
