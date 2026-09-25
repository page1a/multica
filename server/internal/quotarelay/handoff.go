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
	// Branch is the issue's canonical delivery branch, when it has one. The
	// replacement continues there instead of opening a second line.
	Branch string
	// Demotion is set when this opening is the second-or-later breaker in
	// the seat's recent window. Empty when the seat is not being demoted.
	Demotion string
}

func (h Handoff) scope() string {
	if h.Kind == KindSpecializedModel {
		return ScopeModel
	}
	return ScopeAgent
}

func (h Handoff) reasonLabel() string {
	switch h.Kind {
	case KindSpecializedModel:
		return "特化模型额度耗尽"
	case KindProviderCapacity:
		return "模型满载，原地重试已用尽"
	case KindBalanceExhausted:
		return "账号余额耗尽（402），不会自己恢复"
	default:
		return "席位周额度失效"
	}
}

// recovery is the human line for when the failed seat comes back.
func (h Handoff) recovery() string {
	if IsManualRecovery(h.Kind) {
		return "不会自己恢复。要有人去充值或换账号，再手动重新启用这一席"
	}
	return fmt.Sprintf("%s，预计 %s", h.Condition, h.RecoverAt.UTC().Format(time.RFC3339))
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
	switch h.Kind {
	case KindBalanceExhausted:
		fmt.Fprintf(&b, "余额耗尽接力。上一席位「%s」的账号余额用完了（402），已停用且不会自己恢复。请先读这张票的评论和原分支上的提交，从当前进度接着做，不要另开任务，也不要重做已经做完的部分。\n", h.FailedName)
	case KindProviderCapacity:
		fmt.Fprintf(&b, "模型满载接力。上一席位「%s」原地重试后仍然满载，请从这张票的当前讨论继续，不要另开任务。\n", h.FailedName)
	default:
		fmt.Fprintf(&b, "额度熔断接力。上一席位「%s」因%s已停用，请从这张票的当前讨论继续，不要另开任务，也不要重试已经耗尽的额度。\n", h.FailedName, h.reasonLabel())
	}
	fmt.Fprintf(&b, "票 #%d %s。原任务 %s。工作线程 %s。\n", h.IssueNumber, h.IssueTitle, h.TaskID, threadLabel(h.ThreadCommentID))
	fmt.Fprintf(&b, "验收席：%s %s。\n", emptyAsUnset(h.ReviewerType), emptyAsUnset(h.ReviewerID))
	if strings.TrimSpace(h.Acceptance) != "" {
		fmt.Fprintf(&b, "验收标准：%s\n", strings.TrimSpace(h.Acceptance))
	}
	fmt.Fprintf(&b, "恢复条件：%s。上一席位在此之前不会再接这张票。\n", h.recovery())
	if h.Branch != "" {
		fmt.Fprintf(&b, "交付分支：%s。在这条分支上接着做，不要另开分支。\n", h.Branch)
	}
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
	fmt.Fprintf(&b, "- 恢复：%s\n", h.recovery())
	if h.Branch != "" {
		fmt.Fprintf(&b, "- 原分支：%s，新席位在这条分支上接着做\n", h.Branch)
	}
	if h.Demotion != "" {
		fmt.Fprintf(&b, "- 降权：%s\n", h.Demotion)
	}
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
	fmt.Fprintf(&b, "- 恢复：%s。本票改为 blocked，避免停在进行中却没有执行者。\n", h.recovery())
	if h.Demotion != "" {
		fmt.Fprintf(&b, "- 降权：%s\n", h.Demotion)
	}
	fmt.Fprintf(&b, "- %s\n", h.tokens())
	return b.String()
}

// AuditSkip records a breaker that did not move the assignee.
func AuditSkip(h Handoff, why string) string {
	demotion := ""
	if h.Demotion != "" {
		demotion = fmt.Sprintf("- 降权：%s\n", h.Demotion)
	}
	return fmt.Sprintf("## 额度熔断\n\n- 原因：%s\n- 停用席位：%s（%s）。未改派：%s。\n- 恢复：%s\n%s- %s\n",
		h.reasonLabel(), h.FailedName, h.FailedID, why, h.recovery(), demotion, h.tokens())
}

// DemotionLine is the sentence a repeated breaker leaves on the ticket.
func DemotionLine(name string, count int) string {
	return fmt.Sprintf("%s 在 24 小时内熔断了 %d 次。恢复后，新票先派给别的席位，直到这一席自己做成一单。", name, count)
}

// IdleHandoffNote is the opening note on a ticket that had not started
// when its seat broke.
func IdleHandoffNote(fromName, toName string, steppedDown bool) string {
	step := "同档另一家供应商"
	if steppedDown {
		step = "低一档"
	}
	return fmt.Sprintf("席位熔断转派。上一席位「%s」停用时这张票还没开跑，改由「%s」接（%s）。请从这张票的当前讨论继续，不要另开任务。", fromName, toName, step)
}

// AuditIdle records moving a not-yet-started ticket off a broken seat.
// crossHouse is true when the replacement was required to be a different
// provider. steppedDown is true when the same tier had nobody eligible.
func AuditIdle(fromName, toName, toTier string, crossHouse, steppedDown bool, demotion string) string {
	var why string
	switch {
	case steppedDown && crossHouse:
		why = "同档没有别的供应商可接，所以降了一档。"
	case crossHouse:
		why = "同档换了一家供应商。同一家模型多半一起挤，所以不转给同族席位。"
	case steppedDown:
		why = "同档没有可接的席位，所以降了一档。"
	default:
		why = "同档另一席接上。"
	}
	var b strings.Builder
	b.WriteString("## 席位熔断，未开跑的票已转走\n\n")
	fmt.Fprintf(&b, "这张票还没开跑，从「%s」转到「%s」（%s）。%s\n", fromName, toName, toTier, why)
	if demotion != "" {
		fmt.Fprintf(&b, "\n降权：%s\n", demotion)
	}
	return b.String()
}

// BalanceTransferNote is the opening note on a ticket moved off a seat whose
// account ran out of money. started says the old seat already worked on it.
func BalanceTransferNote(fromName, toName, branch string, started bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "席位余额耗尽转派。上一席位「%s」的账号余额用完了（402），已停用且不会自己恢复，这张票改由「%s」接。", fromName, toName)
	if started {
		b.WriteString("上一席已经做过一部分：先读这张票的评论和已有提交，从当前进度接着做，不要另开任务，也不要重做。")
	} else {
		b.WriteString("请从这张票的当前讨论继续，不要另开任务。")
	}
	if branch != "" {
		fmt.Fprintf(&b, "交付分支：%s，在这条分支上继续。", branch)
	}
	return b.String()
}

// AuditBalanceTransfer is the one-sentence explanation left on each ticket
// moved off a seat whose account ran out of money.
func AuditBalanceTransfer(fromName, toName, toTier string, steppedDown, started bool, branch string) string {
	state := "还没开跑"
	if started {
		state = "正在做"
	}
	step := "同档另一家模型"
	if steppedDown {
		step = "同档没有别家可接，降一档的另一家模型"
	}
	var b strings.Builder
	b.WriteString("## 席位余额耗尽，票已转给另一家模型\n\n")
	fmt.Fprintf(&b, "「%s」的账号余额用完了（402），这一席已停用，不会自己恢复。这张票%s，转给「%s」（%s，%s）接着做", fromName, state, toName, toTier, step)
	if branch != "" {
		fmt.Fprintf(&b, "，在原分支 `%s` 上继续", branch)
	}
	fmt.Fprintf(&b, "。「%s」充值恢复后不会把这张票抢回去。\n", fromName)
	return b.String()
}

// BalanceOwnerAlert is the one reminder a workspace owner gets when a seat's
// account runs out of money.
// siblings are the other seats on the same account (the base role's
// specialisations and so on) that went down with it.
func BalanceOwnerAlert(seatName, detail string, moved int, siblings ...string) string {
	var b strings.Builder
	if len(siblings) == 0 {
		fmt.Fprintf(&b, "「%s」的账号余额用完了（402），这一席已自动停用，不会自己恢复。请去充值或给这一席换账号，然后在席位设置里重新启用。", seatName)
	} else {
		fmt.Fprintf(&b, "「%s」的账号余额用完了（402），这一席和用同一个账号的「%s」都已自动停用，不会自己恢复。请去充值或换账号，然后逐个在席位设置里重新启用。", seatName, strings.Join(siblings, "」「"))
	}
	if moved > 0 {
		fmt.Fprintf(&b, "它手上 %d 张票已经转给另一家模型的席位接着做，恢复后不会抢回来。", moved)
	}
	if d := strings.TrimSpace(detail); d != "" {
		fmt.Fprintf(&b, "\n\n供应商原话：%s", d)
	}
	return b.String()
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
