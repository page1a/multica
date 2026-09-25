package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// MaxIssueGroupChildren caps how many sub-issues one group may carry.
//
// It is a payload bound, not a database one: the alignment draft parser
// (internal/handler/issue_draft_group.go) enforces it before any issue exists,
// so an oversized group is refused with a readable error instead of halfway
// through a transaction. The number is deliberately small because a group
// commits as one transaction and every node takes a transaction-scoped advisory
// lock in the duplicate guard, so the group's size is what a concurrent confirm
// blocks on.
const MaxIssueGroupChildren = 20

// IssueGroupNode is one issue of a group: its fully resolved create parameters
// plus the post-commit options its caller's transport supplies.
//
// Options are per node rather than per group because not all of them are
// shared. AnalyticsAgentID follows the node's own assignee, and a group's nodes
// are routinely assigned to different agents.
type IssueGroupNode struct {
	Params IssueCreateParams
	Opts   IssueCreateOpts
}

// IssueGroupParams is one group of issues that commits together: the root
// (parent) first, then its children in the order the caller wants them read
// back. CreateGroup links every other node to the root's inserted row.
//
// When RootIssueID is set the root is already committed: this is a
// continuation round, Nodes are only the nodes that round adds, and every one
// of them becomes a child of that root. The root is never re-inserted, and no
// issue that already exists has a field rewritten — a node of a confirmed group
// may have been edited by a person or an agent since it was created, and a
// follow-up round in the alignment is not a reason to overwrite that. Which
// nodes those are is the caller's decision (the draft handler derives it from
// node identity); by the time CreateGroup runs, Nodes holds inserts only.
// IssueGroupProjectResource is one resource attached to the project this
// group creates. ProjectID is filled in once the project row exists.
type IssueGroupProjectResource struct {
	ResourceType string
	ResourceRef  []byte
	Label        pgtype.Text
	Position     int32
	CreatedBy    pgtype.UUID
}

// IssueGroupProject is a project created in the same transaction as the
// group. Either the project, its resources and every issue commit, or none
// of them do. A directory on disk is not part of this transaction — the
// caller creates it first and deletes it if this call fails.
type IssueGroupProject struct {
	Create    db.CreateProjectParams
	Resources []IssueGroupProjectResource
}

type IssueGroupParams struct {
	Nodes []IssueGroupNode
	// RootIssueID, when valid, is the already-committed root of this group.
	RootIssueID pgtype.UUID
	// Project, when set on a first confirm, is inserted before the issues and
	// becomes every node's project. Ignored on a continuation round: the root
	// already exists and a second project would not be the one it belongs to.
	Project *IssueGroupProject
}

// IssueGroupResult is the committed group. Issues[0] is the root — unless the
// caller passed RootIssueID, in which case the root was already committed and
// Issues holds only the nodes this call inserted.
type IssueGroupResult struct {
	Issues []db.Issue
}

// CreateGroup creates a parent and its children as one committed unit.
//
// The whole group is written in a single application transaction: either every
// node becomes visible or none does. That is why this is one method and not a
// loop over Create — a half-created group is worse than no group at all,
// because it spends the caller's one confirm on a shape nobody agreed to.
//
// The parent/child relationship lives here, in application code. The repository
// adds no foreign keys and no cascading deletes, so nothing but this function
// ties a child to its root and nothing but the delete paths untie them.
//
// The transaction holds no lock outside the issue tables. Callers are expected
// to have finished permission, status and assignee validation before calling:
// a group transaction that waited on unrelated reads would make every
// concurrent confirm block for longer than it has to (see §3.3 and §4.3 of
// docs/design/issue-draft-group-finalize.md).
//
// ErrActiveDuplicate is absent by construction for an alignment group — a
// person who has just read the draft confirms it, so every node passes
// AllowDuplicate — and the guard's row is therefore not surfaced here. Only
// Create has an IssueCreateResult to carry it.
//
// The same transaction serves a continuation round, where group.RootIssueID is
// the group's already-committed root and every node passed in is new. A round
// that adds one node writes one row; a round that adds none does not call this
// at all.
func (s *IssueService) CreateGroup(ctx context.Context, group IssueGroupParams) (IssueGroupResult, error) {
	if len(group.Nodes) == 0 {
		return IssueGroupResult{}, errors.New("issue group has no nodes")
	}

	// Resolved once, before the transaction opens: the policy is a Cloud
	// decision, and resolving it per node could let one group straddle two
	// policies.
	issueCountPolicy := ResolveIssueCountPolicy(ctx, s.Entitlements, group.Nodes[0].Params.WorkspaceID)

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return IssueGroupResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)

	var createdProjectID pgtype.UUID
	if group.Project != nil && !group.RootIssueID.Valid {
		project, err := qtx.CreateProject(ctx, group.Project.Create)
		if err != nil {
			return IssueGroupResult{}, fmt.Errorf("create project: %w", err)
		}
		createdProjectID = project.ID
		for i, res := range group.Project.Resources {
			if _, err := qtx.CreateProjectResource(ctx, db.CreateProjectResourceParams{
				ProjectID:    project.ID,
				WorkspaceID:  project.WorkspaceID,
				ResourceType: res.ResourceType,
				ResourceRef:  res.ResourceRef,
				Label:        res.Label,
				Position:     res.Position,
				CreatedBy:    res.CreatedBy,
			}); err != nil {
				return IssueGroupResult{}, fmt.Errorf("attach project resource %d: %w", i, err)
			}
		}
	}

	issues := make([]db.Issue, 0, len(group.Nodes))
	labels := make([][]db.IssueLabel, 0, len(group.Nodes))
	tasks := make([]db.AgentTaskQueue, 0, len(group.Nodes))
	for i, node := range group.Nodes {
		p := node.Params
		// The project row was inserted in this transaction. The root takes it
		// explicitly; children with no project of their own inherit it from
		// the parent lookup below.
		if createdProjectID.Valid && i == 0 {
			p.ProjectID = createdProjectID
			p.ProjectPinned = true
		}
		switch {
		case group.RootIssueID.Valid:
			// A continuation round: the root is already committed, so nothing
			// in Nodes is the root and every one of them is its child.
			p.ParentIssueID = group.RootIssueID
		case i > 0:
			// The root row was inserted in this very transaction, so
			// createInTx's parent lookup sees it in the same snapshot and
			// back-fills the child's project from it.
			p.ParentIssueID = issues[0].ID
		}
		outcome, err := s.createInTx(ctx, tx, qtx, p, issueCountPolicy, node.Opts)
		if err != nil {
			return IssueGroupResult{}, err
		}
		issues = append(issues, outcome.Issue)
		labels = append(labels, outcome.Labels)
		tasks = append(tasks, outcome.AssignedTask)
	}

	if err := tx.Commit(ctx); err != nil {
		return IssueGroupResult{}, fmt.Errorf("commit: %w", err)
	}

	// Post-commit work runs root first, then the children in the order they
	// were written. That order is the one the payload asked for: the numbers
	// were allocated in it, so it is also the order ListChildIssues reads the
	// group back in. Broadcasting the root first means a client that receives a
	// child's issue:created can already resolve its parent_issue_id.
	for i := range issues {
		s.afterCommit(ctx, issues[i], labels[i], tasks[i], group.Nodes[i].Params, group.Nodes[i].Opts)
	}

	return IssueGroupResult{Issues: issues}, nil
}
