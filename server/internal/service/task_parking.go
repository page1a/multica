package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/parking"
	"github.com/multica-ai/multica/server/internal/progress"
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ParkingSummarizer phrases the one-sentence summary of a parking record when
// the agent left no words of its own. *routing.Router satisfies it. Nil, or
// any error, means the fixed wording is used.
type ParkingSummarizer interface {
	Summarize(ctx context.Context, workspaceID string, f routing.SummaryFacts) (string, error)
}

// ParkingInboxType is the inbox item a person gets when a ticket stops
// without saying why.
const ParkingInboxType = "parking_unexplained"

const parkingRunWindow = 10

// RecordParking writes the issue's parking record at the end of a run
// (DENE-881). It is called from the completion and failure paths only — the
// server never scans for parked tickets (DENE-520) — and it covers every
// status, blocked included. When the verdict is "stopped without saying why"
// and the previous record was not the same verdict, the responsible person
// gets an inbox item.
func (s *TaskService) RecordParking(ctx context.Context, issueID pgtype.UUID, task db.AgentTaskQueue) (parking.Record, bool) {
	issue, err := s.Queries.GetIssue(ctx, issueID)
	if err != nil {
		slog.Warn("parking: load issue failed", "issue_id", util.UUIDToString(issueID), "error", err)
		return parking.Record{}, false
	}
	in, err := s.parkingInput(ctx, issue)
	if err != nil {
		slog.Warn("parking: gather failed", "issue_id", util.UUIDToString(issueID), "error", err)
		return parking.Record{}, false
	}
	rec := parking.Classify(in)
	summary, source := s.parkingSummary(ctx, issue, in, rec)

	prev, prevErr := s.Queries.GetIssueParkingRecord(ctx, issue.ID)
	hadPrev := prevErr == nil
	if prevErr != nil && !errors.Is(prevErr, pgx.ErrNoRows) {
		slog.Warn("parking: load previous record failed", "issue_id", util.UUIDToString(issue.ID), "error", prevErr)
	}

	timeline, _ := json.Marshal(rec.Timeline)
	if rec.Timeline == nil {
		timeline = []byte("[]")
	}
	if _, err := s.Queries.UpsertIssueParkingRecord(ctx, db.UpsertIssueParkingRecordParams{
		IssueID:       issue.ID,
		WorkspaceID:   issue.WorkspaceID,
		State:         rec.State,
		Category:      rec.Category,
		StuckKind:     rec.StuckKind,
		Unexplained:   rec.Unexplained,
		Summary:       summary,
		SummarySource: source,
		NextOwnerType: rec.NextOwner.Type,
		NextOwnerID:   rec.NextOwner.ID,
		IssueStatus:   issue.Status,
		TaskID:        task.ID,
		Timeline:      timeline,
	}); err != nil {
		slog.Warn("parking: write record failed", "issue_id", util.UUIDToString(issue.ID), "error", err)
		return rec, false
	}
	// The parking summary is the last fallback for the issue's progress line
	// (DENE-1037): it fills in only while no agent report or close summary
	// stands — the SQL guard decides, so a report racing this write wins.
	s.recordParkingProgress(ctx, issue, rec, summary, task)

	if rec.Unexplained && !(hadPrev && prev.Unexplained && prev.Category == rec.Category) {
		s.notifyParkingUnexplained(ctx, issue, rec, summary, task)
	}
	return rec, true
}

func (s *TaskService) parkingInput(ctx context.Context, issue db.Issue) (parking.Input, error) {
	in := parking.Input{
		Status: issuestatus.Effective(ctx, s.Queries, issue.WorkspaceID, issue.Status),
		Meta:   issueMetaMap(issue.Metadata),
	}
	if issue.AssigneeType.Valid && issue.AssigneeID.Valid {
		in.Assignee = parking.Owner{Type: issue.AssigneeType.String, ID: util.UUIDToString(issue.AssigneeID)}
	} else {
		in.Assignee = parking.Owner{Type: "none"}
	}
	if issue.ReviewerType.Valid && issue.ReviewerID.Valid && issue.ReviewerType.String != "none" {
		in.Reviewer = parking.Owner{Type: issue.ReviewerType.String, ID: util.UUIDToString(issue.ReviewerID)}
	} else {
		in.Reviewer = parking.Owner{Type: "none"}
	}
	active, err := s.Queries.HasActiveTaskForIssue(ctx, issue.ID)
	if err != nil {
		return in, err
	}
	in.HasActiveTask = active
	in.HasOpenChildren = s.hasOpenChildren(ctx, issue)

	runs, err := s.Queries.ListRecentTasksForParking(ctx, db.ListRecentTasksForParkingParams{IssueID: issue.ID, RowLimit: parkingRunWindow})
	if err != nil {
		return in, err
	}
	for _, r := range runs {
		in.Runs = append(in.Runs, parking.Run{
			ID:            util.UUIDToString(r.ID),
			Status:        r.Status,
			CreatedAt:     r.CreatedAt.Time,
			StartedAt:     r.StartedAt.Time,
			CompletedAt:   r.CompletedAt.Time,
			FailureReason: r.FailureReason.String,
			Error:         r.Error.String,
		})
	}

	prs, err := s.Queries.ListPullRequestsForParking(ctx, issue.ID)
	if err != nil {
		return in, err
	}
	for _, pr := range prs {
		in.PullRequests = append(in.PullRequests, parking.PullRequest{
			Number:   int(pr.PrNumber),
			State:    pr.State,
			URL:      pr.HtmlUrl,
			LinkedAt: pr.LinkedAt.Time,
			MergedAt: pr.MergedAt.Time,
		})
	}

	if len(in.Runs) > 0 {
		since := in.Runs[0].Began()
		rejections, err := s.Queries.ListIssueRejectionsSince(ctx, db.ListIssueRejectionsSinceParams{
			IssueID: issue.ID,
			Since:   pgtype.Timestamptz{Time: since, Valid: true},
		})
		if err != nil {
			return in, err
		}
		for _, r := range rejections {
			in.Rejections = append(in.Rejections, parking.Rejection{
				Action: r.Action, Kind: r.Kind, Reason: r.Reason, At: r.CreatedAt.Time,
			})
		}
	}
	return in, nil
}

// parkingSummary picks, in order: the agent's close evidence, the agent's
// last words in this run, one sentence from the routing model, the fixed
// wording. The model is asked only when the agent's words are missing or are
// just the error echoed back, and only for a parked ticket that is not done.
func (s *TaskService) parkingSummary(ctx context.Context, issue db.Issue, in parking.Input, rec parking.Record) (string, string) {
	const maxRunes = 240
	if rec.CloseCurrent {
		if id := blockwait.MetaString(in.Meta, closeprotocol.KeyEvidenceCommentID); id != "" {
			if cid, err := util.ParseUUID(id); err == nil {
				if c, err := s.Queries.GetComment(ctx, cid); err == nil && c.Content != "" {
					return parking.Clip(c.Content, maxRunes), "close"
				}
			}
		}
	}
	if rec.State == parking.StateRunning || rec.Category == parking.CategoryDone {
		return parking.FallbackSummary(rec), "template"
	}

	var said, runErr string
	if len(in.Runs) > 0 {
		last := in.Runs[0]
		runErr = last.Error
		if tid, err := util.ParseUUID(last.ID); err == nil {
			if c, err := s.Queries.GetLatestAgentCommentForTask(ctx, db.GetLatestAgentCommentForTaskParams{
				IssueID: issue.ID,
				TaskID:  tid,
				Since:   pgtype.Timestamptz{Time: last.Began(), Valid: true},
			}); err == nil {
				said = c.Content
			}
		}
	}
	if !parking.ErrorOnly(said, runErr) {
		return parking.Clip(said, maxRunes), "agent"
	}

	if s.ParkingSummarizer != nil {
		sentence, err := s.ParkingSummarizer.Summarize(ctx, util.UUIDToString(issue.WorkspaceID), routing.SummaryFacts{
			Title:     issue.Title,
			Status:    in.Status,
			Category:  rec.Category,
			Reasons:   rec.Reasons,
			AgentSaid: parking.Clip(said, 400),
			RunError:  parking.Clip(runErr, 400),
		})
		if err == nil && sentence != "" {
			return sentence, "model"
		}
	}
	return parking.FallbackSummary(rec), "template"
}

// notifyParkingUnexplained puts the stop in a person's inbox, not only on the
// ticket: the issue's creator when a person created it, otherwise the
// workspace owner — the same rule routing uses for "who do we @".
func (s *TaskService) notifyParkingUnexplained(ctx context.Context, issue db.Issue, rec parking.Record, summary string, task db.AgentTaskQueue) {
	recipient, ok := s.parkingRecipient(ctx, issue)
	if !ok {
		return
	}
	details, _ := json.Marshal(map[string]any{
		"category":   rec.Category,
		"stuck_kind": rec.StuckKind,
		"task_id":    util.UUIDToString(task.ID),
	})
	actorType := pgtype.Text{String: "system", Valid: true}
	actorID := pgtype.UUID{}
	if task.AgentID.Valid {
		actorType = pgtype.Text{String: "agent", Valid: true}
		actorID = task.AgentID
	}
	item, err := s.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		ID:            dbid.NewV7(),
		WorkspaceID:   issue.WorkspaceID,
		RecipientType: "member",
		RecipientID:   recipient,
		Type:          ParkingInboxType,
		Severity:      "action_required",
		IssueID:       issue.ID,
		Title:         issue.Title,
		Body:          pgtype.Text{String: summary, Valid: summary != ""},
		ActorType:     actorType,
		ActorID:       actorID,
		Details:       details,
	})
	if err != nil {
		slog.Warn("parking: inbox write failed", "issue_id", util.UUIDToString(issue.ID), "error", err)
		return
	}
	if s.Bus != nil {
		s.publishQuickCreateInbox(item, util.UUIDToString(issue.WorkspaceID), util.UUIDToString(task.AgentID), issue.Status)
	}
}

func (s *TaskService) parkingRecipient(ctx context.Context, issue db.Issue) (pgtype.UUID, bool) {
	if issue.CreatorType == "member" && issue.CreatorID.Valid {
		if _, err := s.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
			UserID: issue.CreatorID, WorkspaceID: issue.WorkspaceID,
		}); err == nil {
			return issue.CreatorID, true
		}
	}
	members, err := s.Queries.ListMembers(ctx, issue.WorkspaceID)
	if err != nil {
		slog.Warn("parking: list members failed", "issue_id", util.UUIDToString(issue.ID), "error", err)
		return pgtype.UUID{}, false
	}
	for _, m := range members {
		if m.Role == "owner" {
			return m.UserID, true
		}
	}
	return pgtype.UUID{}, false
}

// recordParkingProgress copies the parking summary onto the issue's progress
// line and pushes issue:updated so boards refresh without a reload. Zero rows
// from the guarded update means an explicit line already stands.
func (s *TaskService) recordParkingProgress(ctx context.Context, issue db.Issue, rec parking.Record, summary string, task db.AgentTaskQueue) {
	text := progress.Clip(summary)
	if text == "" {
		return
	}
	tone := progress.ForParking(rec.Category)
	updated, err := s.Queries.UpdateIssueProgress(ctx, db.UpdateIssueProgressParams{
		ID: issue.ID, WorkspaceID: issue.WorkspaceID, Text: text, Source: parkingProgressSource, Tone: tone,
		AuthorType: "system", AuthorID: task.AgentID, FallbackOnly: true,
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("parking: write progress failed", "issue_id", util.UUIDToString(issue.ID), "error", err)
		}
		return
	}
	if issue.ProgressSource == parkingProgressSource && issue.ProgressText == text && issue.ProgressTone == tone {
		// Same line as before: no history row, no broadcast.
		return
	}
	if err := s.Queries.CreateIssueProgress(ctx, db.CreateIssueProgressParams{
		WorkspaceID: issue.WorkspaceID, IssueID: issue.ID, Text: text, Source: parkingProgressSource, Tone: tone,
		AuthorType: "system", AuthorID: task.AgentID,
	}); err != nil {
		slog.Warn("parking: write progress history failed", "issue_id", util.UUIDToString(issue.ID), "error", err)
	}
	if s.Bus != nil {
		s.Bus.Publish(events.Event{
			Type:        protocol.EventIssueUpdated,
			WorkspaceID: util.UUIDToString(updated.WorkspaceID),
			ActorType:   "system",
			Payload: map[string]any{
				"issue":            IssueToMapResolved(ctx, s.Queries, updated, s.getIssuePrefix(updated.WorkspaceID)),
				"status_changed":   false,
				"prev_status":      updated.Status,
				"progress_changed": true,
			},
		})
	}
}

// parkingProgressSource tags progress copied from the parking summary. The
// summary's own source (model / fixed wording) stays on the parking record.
const parkingProgressSource = "parking"
