package routing

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The routing comment IS the decision log. There is no second log file: a
// separate one would have to be found, would not be readable by the person the
// decision affects, and would drift from what the ticket actually shows.

const changeSlotFooter = "改右侧任一格即可，改完那一格不会被自动改回。"

func pct(v float64) string {
	return strconv.FormatFloat(v*100, 'f', 0, 64) + "%"
}

func directionLine(issue Issue, match DirectionMatch) string {
	switch {
	case issue.ProjectName == "":
		return "- **方向**：未知（本票没有所属 project），从通用档位里选"
	case match.Invalid != "":
		return fmt.Sprintf("- **方向**：未知（对照表把 project「%s」写成了「%s」，但没有这个方向），从通用档位里选",
			issue.ProjectName, match.Invalid)
	case !match.Known:
		return fmt.Sprintf("- **方向**：未知（project「%s」不在对照表里），从通用档位里选", issue.ProjectName)
	case match.Direction == "":
		return fmt.Sprintf("- **方向**：%s（对照表把 project「%s」归为通用），从通用档位里选", GenericDirection, issue.ProjectName)
	}
	return fmt.Sprintf("- **方向**：%s（来自 project「%s」）", match.Direction, issue.ProjectName)
}

// assignmentComment is the todo-row decision comment: what went into each
// slot, where the direction came from, the confidence against the threshold,
// and the verdict.
func (r *Router) assignmentComment(
	issue Issue,
	match DirectionMatch,
	candidates []Seat,
	v Verdict,
	threshold float64,
	executor *Seat,
	executorSource string,
	reviewer ReviewerRef,
	reviewerFallback bool,
	fallbackWhy string,
	humanSignoff bool,
	needExecutor, needReviewer bool,
	stillUnassigned bool,
) string {
	var b strings.Builder
	b.WriteString("## 自动选派\n\n")

	// Executor slot.
	switch {
	case !needExecutor:
		b.WriteString("- **执行席**：由你指定，未改动\n")
	case executor != nil && executorSource == pickLabel:
		b.WriteString(fmt.Sprintf("- **执行席**：%s（%s档，按票上的「%s」标签选的，没问模型）→ 已派出，run 已启动\n",
			executor.Name, executor.TierLabel, executor.TierLabel))
	case executor != nil && executorSource == pickFallback:
		b.WriteString(fmt.Sprintf("- **执行席**：%s（%s档，**兜底档**——裁决置信度 %s 低于阈值 %s，或它点的档位这里没有席位）→ 仍然派出，run 已启动。觉得档位不对直接改，改了路由不会再碰\n",
			executor.Name, executor.TierLabel, pct(v.ExecutorConfidence), pct(threshold)))
	case executor != nil:
		b.WriteString(fmt.Sprintf("- **执行席**：%s（%s档，置信度 %s ≥ 阈值 %s）→ 已派出，run 已启动\n",
			executor.Name, executor.TierLabel, pct(v.ExecutorConfidence), pct(threshold)))
	default:
		b.WriteString("- **执行席**：⚠️ 未填——这一格在本次裁决与写入之间被别人占了\n")
	}

	// Reviewer slot.
	switch {
	case !needReviewer:
		b.WriteString(fmt.Sprintf("- **验收席**：已有值「%s」，未改动\n", issue.Reviewer.Label()))
	case reviewerFallback && humanSignoff && !reviewer.Empty():
		b.WriteString(fmt.Sprintf("- **验收席**：%s（模型判为这次验收需要人拍板，置信度 %s。验收席不填人——填了人这张票后面就没人能推动了；%s）\n",
			reviewer.Label(), pct(v.ReviewerConfidence), fallbackWhy))
	case reviewerFallback && !reviewer.Empty():
		b.WriteString(fmt.Sprintf("- **验收席**：%s（**兜底**——裁决置信度 %s 低于阈值 %s，%s）\n",
			reviewer.Label(), pct(v.ReviewerConfidence), pct(threshold), fallbackWhy))
	case reviewer.Kind == ReviewerNoReview:
		b.WriteString(fmt.Sprintf("- **验收席**：本票不需要验收（置信度 %s）。要人复核就自己填一个\n", pct(v.ReviewerConfidence)))
	case !reviewer.Empty():
		b.WriteString(fmt.Sprintf("- **验收席**：%s（置信度 %s ≥ 阈值 %s）\n",
			reviewer.Label(), pct(v.ReviewerConfidence), pct(threshold)))
	default:
		b.WriteString("- **验收席**：⚠️ 未填——这一格在本次裁决与写入之间被别人占了\n")
	}

	b.WriteString(directionLine(issue, match))
	b.WriteString("\n")
	b.WriteString("- **候选**：" + seatNames(candidates) + "\n")
	if strings.TrimSpace(v.Reason) != "" {
		b.WriteString("- **判断**：" + strings.TrimSpace(v.Reason) + "\n")
	}
	b.WriteString("\n")

	if humanSignoff && reviewer.Kind == ReviewerAgent {
		b.WriteString(fmt.Sprintf("这次验收里有需要人拍板的部分：**%s** 先做检查、合并、关票，遇到只有人能定的事，由它在票下 @ 对应的人。票不会被改派给人。\n\n",
			reviewer.Label()))
	}

	if stillUnassigned {
		b.WriteString("⚠️ 执行席没派出去，这张票会一直躺在待办，所以 @ 你一次。\n\n")
	}
	b.WriteString(changeSlotFooter)
	b.WriteString("\n\n**路由没有改过状态** —— 状态是事实，只有干活的席位知道。")
	return b.String()
}

// handoffComment is the in-review-row comment. Every handoff it describes is
// to a seat: the reviewer slot never names a person.
func (r *Router) handoffComment(issue Issue, to string, decidedHere bool) string {
	var b strings.Builder
	b.WriteString("## 交接\n\n")
	b.WriteString("这张票进入了待验收。\n\n")
	from := "原执行席"
	if issue.AssigneeType == "" {
		from = "无人"
	}
	b.WriteString(fmt.Sprintf("- 已从 %s 改派给 **%s**\n", from, to))
	if decidedHere {
		b.WriteString("- 这张票之前没有验收席，路由在它进入待验收时现场定了一个并填上\n")
	}
	b.WriteString("- 指派本身就是叫醒，这个席位的 run 已启动\n")
	b.WriteString("\n验收席只做检查、合并、关票，不重做这张票的活；认为要返工就把票改回执行席并说明原因。遇到只有人能定的事，在票下 @ 对应的人，不要把票改派给人。\n")
	b.WriteString("\n**路由没有改过状态。**")
	return b.String()
}

// reviewerIsPersonComment is the in-review-row comment for a ticket whose
// reviewer slot names a person — filled by hand, or by a routing version that
// still wrote people into it. Nothing is reassigned: a ticket a person holds
// is a ticket routing never touches again, so handing it over is what freezes
// it. The person is notified and the ticket stays where it is.
func (r *Router) reviewerIsPersonComment(issue Issue, target Member, decidedHere bool) string {
	var b strings.Builder
	name := strings.TrimSpace(target.Name)
	if name == "" {
		name = "这个人"
	}
	b.WriteString("## 待验收\n\n")
	b.WriteString(fmt.Sprintf("这张票进入了待验收，验收席填的是人（%s），所以 @ 一次。\n\n", name))
	if decidedHere {
		b.WriteString("- 这张票之前没有验收席，路由在它进入待验收时现场定了一个并填上\n")
	}
	b.WriteString("- **票没有被改派**：改派给人之后路由就再也不碰这张票，状态也就没人能往下推了\n")
	b.WriteString("- 验收完把票改成完成，或者写清楚要返工的地方并改回执行席\n")
	b.WriteString("- 不想再被 @，把验收席改成一个席位或「不需要验收」即可\n")
	b.WriteString("\n**路由没有改过状态，也没有改过负责人。**")
	return b.String()
}

// adviceComment is the blocked-row comment. It must state, in so many words,
// that nothing was changed — an advisory comment that looks like an action is
// worse than none.
func (r *Router) adviceComment(issue Issue, a Advice, candidates []Seat) string {
	var b strings.Builder
	b.WriteString("## 建议\n\n")
	switch a.Cause {
	case "tier":
		seatName := a.SuggestedTier
		if s, ok := SeatByTier(candidates, a.SuggestedTier); ok {
			seatName = fmt.Sprintf("%s（%s档）", s.Name, s.TierLabel)
		}
		b.WriteString("判断：像是档位不够导致的卡点。\n\n")
		b.WriteString("建议：换 " + seatName + " 再试。\n")
	case "human":
		b.WriteString("判断：这个卡点需要人来拍板。\n\n")
	default:
		b.WriteString("判断：卡点不像档位问题，也不像单纯等人。\n\n")
	}
	if strings.TrimSpace(a.Reason) != "" {
		b.WriteString("\n" + strings.TrimSpace(a.Reason) + "\n")
	}
	b.WriteString("\n**没有改动任何值** —— 执行席、验收席、状态都保持原样。这条只是建议，@ 你一次是因为票卡住了就没人会动它。")
	return b.String()
}

// stalledComment is the stale-review row's comment, and it is only ever posted
// when the reviewer is a person: a seat is woken by being assigned, a person is
// woken by being told. It says once what has been quiet and for how long, and
// it says plainly that nothing was changed — the whole reason this comment can
// exist at all is that the row took no action beyond speaking.
func (r *Router) stalledComment(issue Issue, d StaleDecision, quiet time.Duration) string {
	var b strings.Builder
	b.WriteString("## 停在待验收\n\n")
	b.WriteString(fmt.Sprintf("这张票在待验收里静了 **%s**，期间没有任何 run 在跑，验收席是 **%s**（你）。\n\n",
		quietLabel(quiet), issue.Reviewer.Label()))
	if strings.TrimSpace(d.Reason) != "" {
		b.WriteString("- **判断**：" + strings.TrimSpace(d.Reason) + "\n")
	}
	b.WriteString("- 票上没有找到你给出的通过结论，所以路由不会替你宣布验收完成\n")
	b.WriteString("\n验收完就把状态改成完成；要返工就改回执行席并说明原因。\n")
	b.WriteString("\n**没有改动任何值** —— 负责人、验收席、状态都保持原样。这条只提醒一次，之后不会再刷。")
	return b.String()
}

// completedComment records the only status write this package performs, and it
// has to show its work: which remark it read as the acceptance verdict, how
// confident that read was, and the fact that the verdict was somebody else's.
func (r *Router) completedComment(issue Issue, d StaleDecision, threshold float64) string {
	var b strings.Builder
	b.WriteString("## 状态对齐到已有结论\n\n")
	b.WriteString(fmt.Sprintf("验收席 **%s** 已经在这张票上给出通过结论，但状态一直停在待验收，所以路由把状态改成了**完成**。\n\n",
		issue.Reviewer.Label()))
	if strings.TrimSpace(d.Reason) != "" {
		b.WriteString("- **依据**：" + strings.TrimSpace(d.Reason) + "\n")
	}
	b.WriteString(fmt.Sprintf("- **置信度**：%s ≥ 阈值 %s\n", pct(d.Confidence), pct(threshold)))
	b.WriteString("- **改动**：只改了状态。负责人、执行席、验收席一律没碰\n")
	b.WriteString("\n路由不会自己判断活有没有干完 —— 它只在票面上已经有验收结论时，把状态对齐到这个事实。判断错了直接把状态改回去，改完路由不会再碰这张票。")
	return b.String()
}

// quietLabel renders a stall duration the way a person would say it.
func quietLabel(d time.Duration) string {
	h := int(d.Hours())
	if h < 48 {
		return strconv.Itoa(h) + " 小时"
	}
	return strconv.Itoa(h/24) + " 天"
}

// unavailableComment is posted once when a configured model cannot be reached.
// After it, the breaker keeps the workspace quiet until the cooldown elapses.
func (r *Router) unavailableComment(issue Issue) string {
	return "## 路由未生效\n\n" +
		"路由模型这次没能给出判断，所以这张票**一个值都没有被写入**，执行席和验收席都保持原样。\n\n" +
		"原因只在设置的「路由」一节里显示，票上不会反复留言：连续失败后路由会进入冷却，冷却期内不再发请求。"
}

func seatNames(seats []Seat) string {
	if len(seats) == 0 {
		return "（空）"
	}
	names := make([]string, 0, len(seats))
	for _, s := range seats {
		names = append(names, fmt.Sprintf("%s(%s)", s.Name, s.TierLabel))
	}
	return strings.Join(names, " · ")
}
