package quotarelay

import (
	"fmt"
	"strings"
	"time"
)

// Handoff is the context the replacement run, and the issue audit comment,
// must carry. The new agent does not resume the failed seat's session; it
// continues the same issue and comment thread.
type Handoff struct {
	FailedName      string
	FailedID        string
	TaskID          string
	IssueNumber     int32
	IssueTitle      string
	ThreadCommentID string
	ReviewerType    string
	ReviewerID      string
	Acceptance      string
	Kind            Kind
	ModelKey        string
	RecoverAt       time.Time
	Condition       string
	ReplacementName string
	ReplacementTier string
	SteppedDown     bool
	WaitReason      string
}

func (h Handoff) scope() string {
	if h.Kind == KindSpecializedModel {
		return ScopeModel
	}
	return ScopeAgent
}

func (h Handoff) reasonLabel() string {
	if h.Kind == KindSpecializedModel {
		return "特化模型额度耗尽"
	}
	return "席位周额度失效"
}

func (h Handoff) step() string {
	if h.SteppedDown {
		return "down"
	}
	return "same"
}

func (h Handoff) tokens() string {
	thread := h.ThreadCommentID
	if thread == "" {
		thread = "issue"
	}
	reviewer := h.ReviewerID
	if reviewer == "" {
		reviewer = "unset"
	}
	return fmt.Sprintf(
		"scope=%s from_task=%s thread=%s reviewer_id=%s reviewer_type=%s recover_at=%s step=%s model_key=%s",
		h.scope(), h.TaskID, thread, reviewer, emptyAsUnset(h.ReviewerType),
		h.RecoverAt.UTC().Format(time.RFC3339), h.step(), emptyAsUnset(h.ModelKey),
	)
}

func emptyAsUnset(v string) string {
	if strings.TrimSpace(v) == "" {
		return "unset"
	}
	return v
}

// AgentNote is the opening handoff on the replacement task.
func AgentNote(h Handoff) string {
	var b strings.Builder
	fmt.Fprintf(&b, "额度熔断接力。上一席位「%s」因%s已停用，请从这张票的当前讨论继续，不要另开任务，也不要重试已经耗尽的额度。\n", h.FailedName, h.reasonLabel())
	fmt.Fprintf(&b, "票 #%d %s。原任务 %s。工作线程 %s。\n", h.IssueNumber, h.IssueTitle, h.TaskID, threadLabel(h.ThreadCommentID))
	fmt.Fprintf(&b, "验收席：%s %s。\n", emptyAsUnset(h.ReviewerType), emptyAsUnset(h.ReviewerID))
	if strings.TrimSpace(h.Acceptance) != "" {
		fmt.Fprintf(&b, "验收标准：%s\n", strings.TrimSpace(h.Acceptance))
	}
	fmt.Fprintf(&b, "恢复条件：%s，预计 %s。上一席位在此之前不会再接这张票。\n", h.Condition, h.RecoverAt.UTC().Format(time.RFC3339))
	b.WriteString(h.tokens())
	return b.String()
}

// AuditRelay is the issue comment that records a successful handoff.
func AuditRelay(h Handoff) string {
	var b strings.Builder
	b.WriteString("## 额度熔断接力\n\n")
	fmt.Fprintf(&b, "- 原因：%s\n", h.reasonLabel())
	fmt.Fprintf(&b, "- 停用席位：%s（%s）。只停这一席，共享模型配置和其他席位未改。\n", h.FailedName, h.FailedID)
	fmt.Fprintf(&b, "- 接力：%s（%s，%s）\n", h.ReplacementName, h.ReplacementTier, stepLabel(h.SteppedDown))
	fmt.Fprintf(&b, "- 恢复：%s，预计 %s\n", h.Condition, h.RecoverAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- %s\n", h.tokens())
	return b.String()
}

// AuditWait is the issue comment when no replacement seat exists.
func AuditWait(h Handoff) string {
	var b strings.Builder
	b.WriteString("## 额度熔断后无人可接力\n\n")
	fmt.Fprintf(&b, "- 原因：%s\n", h.reasonLabel())
	fmt.Fprintf(&b, "- 停用席位：%s（%s）。只停这一席，共享模型配置和其他席位未改。\n", h.FailedName, h.FailedID)
	fmt.Fprintf(&b, "- 等待：%s\n", h.WaitReason)
	fmt.Fprintf(&b, "- 恢复：%s，预计 %s。本票改为 blocked，避免停在进行中却没有执行者。\n", h.Condition, h.RecoverAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- %s\n", h.tokens())
	return b.String()
}

// AuditSkip records a breaker that did not move the assignee.
func AuditSkip(h Handoff, why string) string {
	return fmt.Sprintf("## 额度熔断\n\n- 原因：%s\n- 停用席位：%s（%s）。未改派：%s。\n- 恢复：%s，预计 %s\n- %s\n",
		h.reasonLabel(), h.FailedName, h.FailedID, why, h.Condition, h.RecoverAt.UTC().Format(time.RFC3339), h.tokens())
}

func threadLabel(id string) string {
	if id == "" {
		return "整张票"
	}
	return id
}

func stepLabel(down bool) string {
	if down {
		return "降一档"
	}
	return "同档"
}
