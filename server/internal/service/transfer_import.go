package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/entitlement"
	"github.com/multica-ai/multica/server/internal/storage"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type TransferImportEnv struct {
	Queries    *db.Queries
	TxStarter  TxStarter
	TargetID   pgtype.UUID
	TargetSlug string
	ImporterID pgtype.UUID
	PublicURL  string
	Storage    storage.Storage
	// Entitlements is the workspace-scoped commercial gate. It is read (never
	// enforced) by the V3 task import to echo the issue quota in its report.
	Entitlements entitlement.Provider
}

func ImportTransferConfig(ctx context.Context, env TransferImportEnv, req TransferConfigRequest) (*TransferConfigReport, error) {
	if req.Manifest.Format != "" && req.Manifest.Format != TransferBundleFormat {
		return nil, &ImportError{Status: 400, Code: "transfer_bundle_invalid", Msg: "manifest format is not multica.workspace-transfer"}
	}
	if req.Manifest.Format != "" && req.Manifest.SchemaVersion != 0 &&
		req.Manifest.SchemaVersion != TransferBundleSchemaVersionV1 &&
		req.Manifest.SchemaVersion != TransferBundleSchemaVersionV2 {
		return nil, &ImportError{Status: 400, Code: "transfer_bundle_version_unsupported", Msg: fmt.Sprintf(
			"unsupported transfer schema_version %d: this server understands up to %d, so the bundle was produced by a newer build than the one running here — upgrade this target server",
			req.Manifest.SchemaVersion, TransferBundleSchemaVersion)}
	}
	if req.DryRun == nil {
		return nil, &ImportError{Status: 400, Code: "transfer_bundle_invalid", Msg: "dry_run is required"}
	}

	peopleMap, peopleRows := mapPeople(ctx, env, req)
	rewriteBundleMembers(&req.Config, peopleMap, req.Manifest.Source.ExportedBy, uuidString(env.ImporterID))
	if req.Config.Source.ExportedBy == "" {
		req.Config.Source.ExportedBy = uuidString(env.ImporterID)
	}

	cfgReq := ConfigImportRequest{
		Bundle:     req.Config,
		DryRun:     req.DryRun,
		OnConflict: req.OnConflict,
		Options:    req.Options,
	}
	cfgReport, err := ImportWorkspaceConfig(ctx, ConfigImportEnv{
		Queries:    env.Queries,
		TxStarter:  env.TxStarter,
		TargetID:   env.TargetID,
		TargetSlug: env.TargetSlug,
		ImporterID: env.ImporterID,
		PublicURL:  env.PublicURL,
	}, cfgReq)
	if err != nil {
		return nil, err
	}

	profileItems, err := importRuntimeProfiles(ctx, env, req, *req.DryRun)
	if err != nil {
		return nil, err
	}
	pinnedItems, err := importPinnedAgents(ctx, env, req, *req.DryRun)
	if err != nil {
		return nil, err
	}
	runtimeBinds, err := planRuntimeBindings(ctx, env, req, cfgReport)
	if err != nil {
		return nil, err
	}

	report := &TransferConfigReport{
		ConfigReport:   cfgReport,
		PeopleMap:      peopleRows,
		Profiles:       profileItems,
		PinnedAgents:   pinnedItems,
		RuntimesToBind: runtimeBinds,
		ExportGaps:     req.Manifest.ExportGaps,
	}
	return report, nil
}

func mapPeople(ctx context.Context, env TransferImportEnv, req TransferConfigRequest) (map[string]string, []TransferPeopleMapRow) {
	out := map[string]string{}
	rows := []TransferPeopleMapRow{}
	exporter := req.Manifest.Source.ExportedBy
	importer := uuidString(env.ImporterID)
	if exporter != "" {
		out[exporter] = importer
	}
	for _, p := range req.People {
		row := TransferPeopleMapRow{SourceUserID: p.SourceUserID}
		if p.SourceUserID == exporter {
			row.Mapped = true
			row.TargetUserID = importer
			rows = append(rows, row)
			continue
		}
		email := strings.TrimSpace(p.Email)
		if email == "" {
			row.Reason = "member_unmapped"
			rows = append(rows, row)
			continue
		}
		m, err := env.Queries.GetWorkspaceMemberByEmail(ctx, db.GetWorkspaceMemberByEmailParams{
			WorkspaceID: env.TargetID,
			Email:       email,
		})
		if err != nil {
			row.Reason = "member_unmapped"
			rows = append(rows, row)
			continue
		}
		tid := uuidString(m.UserID)
		out[p.SourceUserID] = tid
		row.Mapped = true
		row.TargetUserID = tid
		rows = append(rows, row)
	}
	return out, rows
}

func rewriteBundleMembers(bundle *ConfigBundle, peopleMap map[string]string, exporter, importer string) {
	mapID := func(id string) string {
		if id == "" {
			return id
		}
		if id == exporter && importer != "" {
			return importer
		}
		if t, ok := peopleMap[id]; ok {
			return t
		}
		return id
	}
	for i := range bundle.Entities.Agents {
		for j := range bundle.Entities.Agents[i].InvocationTargets {
			t := &bundle.Entities.Agents[i].InvocationTargets[j]
			if t.TargetType == "member" && t.TargetID != nil {
				mapped := mapID(*t.TargetID)
				t.TargetID = &mapped
			}
		}
	}
	for i := range bundle.Entities.Squads {
		for j := range bundle.Entities.Squads[i].Members {
			m := &bundle.Entities.Squads[i].Members[j]
			if m.MemberType == "member" {
				m.MemberID = mapID(m.MemberID)
			}
		}
	}
	for i := range bundle.Entities.Autopilots {
		for j := range bundle.Entities.Autopilots[i].Subscribers {
			s := &bundle.Entities.Autopilots[i].Subscribers[j]
			if s.UserType == "member" {
				s.UserID = mapID(s.UserID)
			}
		}
		for j := range bundle.Entities.Autopilots[i].Collaborators {
			c := &bundle.Entities.Autopilots[i].Collaborators[j]
			if c.UserType == "member" {
				c.UserID = mapID(c.UserID)
			}
		}
	}
	for i := range bundle.Entities.Projects {
		if bundle.Entities.Projects[i].Lead != nil && bundle.Entities.Projects[i].Lead.Type == "member" {
			bundle.Entities.Projects[i].Lead.ID = mapID(bundle.Entities.Projects[i].Lead.ID)
		}
	}
	for i := range bundle.Entities.QuickActions {
		if bundle.Entities.QuickActions[i].Assignee != nil && bundle.Entities.QuickActions[i].Assignee.Type == "member" {
			bundle.Entities.QuickActions[i].Assignee.ID = mapID(bundle.Entities.QuickActions[i].Assignee.ID)
		}
	}
}

func importRuntimeProfiles(ctx context.Context, env TransferImportEnv, req TransferConfigRequest, dry bool) ([]ConfigImportItem, error) {
	onConflict := req.OnConflict
	if onConflict == "" {
		onConflict = ConflictFail
	}
	items := []ConfigImportItem{}
	for _, p := range req.RuntimeProfiles.Profiles {
		existing, err := env.Queries.GetRuntimeProfileByDisplayName(ctx, db.GetRuntimeProfileByDisplayNameParams{
			WorkspaceID: env.TargetID,
			DisplayName: p.DisplayName,
		})
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("lookup runtime profile: %w", err)
		}
		item := ConfigImportItem{SourceID: p.SourceID, Name: p.DisplayName}
		if exists {
			switch onConflict {
			case ConflictSkip:
				item.Action = ActionSkipped
				item.TargetID = uuidString(existing.ID)
				item.Reason = "already exists"
				items = append(items, item)
				continue
			case ConflictFail:
				return nil, &ImportError{Status: 409, Code: "config_import_conflict", Msg: "runtime profile already exists: " + p.DisplayName}
			case ConflictRename:
				p.DisplayName = p.DisplayName + " (imported)"
				item.Action = ActionRenamed
			case ConflictOverwrite:
				item.Action = ActionUpdated
				item.TargetID = uuidString(existing.ID)
				if dry {
					items = append(items, item)
					continue
				}
				args, _ := json.Marshal(p.FixedArgs)
				args = restoreSecretArgs(args, existing.FixedArgs)
				_, err := env.Queries.UpdateRuntimeProfile(ctx, db.UpdateRuntimeProfileParams{
					DisplayName: pgtype.Text{String: p.DisplayName, Valid: true},
					CommandName: pgtype.Text{String: p.CommandName, Valid: p.CommandName != ""},
					Description: pgtype.Text{String: p.Description, Valid: p.Description != ""},
					FixedArgs:   args,
					Enabled:     pgtype.Bool{Bool: p.Enabled, Valid: true},
					ID:          existing.ID,
					WorkspaceID: env.TargetID,
				})
				if err != nil {
					return nil, fmt.Errorf("update runtime profile: %w", err)
				}
				items = append(items, item)
				continue
			}
		} else {
			item.Action = ActionCreated
		}
		if dry {
			items = append(items, item)
			continue
		}
		args, _ := json.Marshal(p.FixedArgs)
		args = restoreSecretArgs(args, nil)
		vis := p.Visibility
		if vis == "" {
			vis = "workspace"
		}
		created, err := env.Queries.CreateRuntimeProfile(ctx, db.CreateRuntimeProfileParams{
			WorkspaceID:    env.TargetID,
			DisplayName:    p.DisplayName,
			ProtocolFamily: p.ProtocolFamily,
			CommandName:    p.CommandName,
			Description:    pgtype.Text{String: p.Description, Valid: p.Description != ""},
			FixedArgs:      args,
			Visibility:     vis,
			CreatedBy:      env.ImporterID,
			Enabled:        p.Enabled,
		})
		if err != nil {
			return nil, fmt.Errorf("create runtime profile: %w", err)
		}
		item.TargetID = uuidString(created.ID)
		items = append(items, item)
	}
	return items, nil
}

func importPinnedAgents(ctx context.Context, env TransferImportEnv, req TransferConfigRequest, dry bool) ([]ConfigImportItem, error) {
	items := []ConfigImportItem{}
	for _, pin := range req.Preferences.PinnedAgents {
		item := ConfigImportItem{SourceID: pin.AgentID, Name: pin.AgentID, Action: ActionCreated}
		agent, err := env.Queries.GetUserAgentByName(ctx, db.GetUserAgentByNameParams{
			WorkspaceID: env.TargetID,
			Name:        pin.AgentID, // may be source id; try name via config report instead
		})
		if err != nil {
			// Prefer mapping by imported agent name from config entities.
			mapped := false
			for _, a := range req.Config.Entities.Agents {
				if a.SourceID == pin.AgentID {
					agent, err = env.Queries.GetUserAgentByName(ctx, db.GetUserAgentByNameParams{
						WorkspaceID: env.TargetID,
						Name:        a.Name,
					})
					if err == nil {
						mapped = true
					}
					break
				}
			}
			if !mapped {
				item.Action = ActionSkipped
				item.Reason = "agent_unmapped"
				items = append(items, item)
				continue
			}
		}
		item.TargetID = uuidString(agent.ID)
		if dry {
			items = append(items, item)
			continue
		}
		_, err = env.Queries.CreateChatPinnedAgent(ctx, db.CreateChatPinnedAgentParams{
			WorkspaceID: env.TargetID,
			UserID:      env.ImporterID,
			AgentID:     agent.ID,
			Position:    pin.Position,
		})
		if err != nil {
			return nil, fmt.Errorf("pin agent: %w", err)
		}
		items = append(items, item)
	}
	return items, nil
}

// planRuntimeBindings is the three-tier binding rule from DENE-364. For every
// agent the config import wrote it resolves the source runtime the agent ran on
// and matches it against the target runtimes the importer can see, on the three
// facts a migration is expected to reproduce: provider, runtime mode, and
// custom profile name.
//
// The plan never guesses. A single candidate is offered to the auto-bind rule
// (the handler writes it when auto_bind_runtimes is on), several candidates
// stay `pending` for the Desktop card to ask about, and none records a
// readable reason instead of disappearing.
func planRuntimeBindings(ctx context.Context, env TransferImportEnv, req TransferConfigRequest, cfg *ConfigImportReport) ([]TransferRuntimeBind, error) {
	out := []TransferRuntimeBind{}
	if cfg == nil {
		return out, nil
	}

	hintByAgent := map[string]TransferAgentRuntimeHint{}
	for _, h := range req.RuntimeProfiles.AgentHints {
		if h.SourceAgentID != "" {
			hintByAgent[h.SourceAgentID] = h
		}
	}

	// Owner-scoped visibility: a private runtime belongs to another member's
	// machine, so it can never be a binding target for the importer even when
	// the importer is a workspace admin (same gate the agent editor applies).
	runtimes, err := env.Queries.ListVisibleAgentRuntimes(ctx, db.ListVisibleAgentRuntimesParams{
		WorkspaceID: env.TargetID,
		OwnerID:     env.ImporterID,
	})
	if err != nil {
		return nil, fmt.Errorf("list target runtimes: %w", err)
	}
	profiles, err := env.Queries.ListRuntimeProfiles(ctx, env.TargetID)
	if err != nil {
		return nil, fmt.Errorf("list target runtime profiles: %w", err)
	}
	profileName := map[string]string{}
	for _, p := range profiles {
		profileName[uuidString(p.ID)] = p.DisplayName
	}

	for _, batch := range cfg.Batches {
		if batch.EntityType != "agents" {
			continue
		}
		for _, item := range batch.Items {
			if item.Action == ActionSkipped || item.Action == ActionFailed {
				continue
			}
			out = append(out, planAgentRuntimeBind(req, hintByAgent, runtimes, profileName, item))
		}
	}
	return out, nil
}

func planAgentRuntimeBind(
	req TransferConfigRequest,
	hintByAgent map[string]TransferAgentRuntimeHint,
	runtimes []db.AgentRuntime,
	profileName map[string]string,
	item ConfigImportItem,
) TransferRuntimeBind {
	bind := TransferRuntimeBind{
		SourceAgentID: item.SourceID,
		AgentTargetID: item.TargetID,
		AgentName:     item.Name,
		Status:        RuntimeBindNoCandidate,
	}

	hint, ok := hintByAgent[item.SourceID]
	if !ok || hint.Provider == "" {
		// Bundles exported before DENE-364 carry no per-agent runtime hint.
		// Binding without knowing the provider would put an agent on a machine
		// that cannot run it, so say why instead.
		bind.ReasonCode = RuntimeBindReasonProviderUnknown
		bind.Reason = "the bundle carries no source runtime for this agent; re-export it from the source environment"
		return bind
	}

	bind.Provider = hint.Provider
	bind.RuntimeMode = hint.RuntimeMode
	bind.ProfileName = hint.ProfileName

	for _, rt := range runtimes {
		if rt.Provider != hint.Provider {
			continue
		}
		if hint.RuntimeMode != "" && rt.RuntimeMode != hint.RuntimeMode {
			continue
		}
		targetProfile := ""
		if rt.ProfileID.Valid {
			targetProfile = profileName[uuidString(rt.ProfileID)]
		}
		// Built-in runtimes carry no profile, custom ones carry exactly one, so
		// comparing the display names covers both: an agent that ran on a
		// built-in CLI must not be pointed at a custom profile and vice versa.
		if targetProfile != hint.ProfileName {
			continue
		}
		name := rt.Name
		if rt.CustomName.Valid && rt.CustomName.String != "" {
			name = rt.CustomName.String
		}
		bind.CandidateIDs = append(bind.CandidateIDs, uuidString(rt.ID))
		bind.Candidates = append(bind.Candidates, TransferRuntimeCandidate{
			ID:          uuidString(rt.ID),
			Name:        name,
			Provider:    rt.Provider,
			RuntimeMode: rt.RuntimeMode,
			ProfileName: targetProfile,
		})
	}

	switch len(bind.Candidates) {
	case 0:
		bind.ReasonCode = RuntimeBindReasonNoRuntime
		bind.Reason = noRuntimeCandidateReason(hint)
	default:
		bind.Status = RuntimeBindPending
	}
	return bind
}

// noRuntimeCandidateReason spells out the one action that fixes the gap: the
// target instance is missing the machine the source agents ran on.
func noRuntimeCandidateReason(hint TransferAgentRuntimeHint) string {
	desc := "provider=" + hint.Provider
	if hint.RuntimeMode != "" {
		desc += " runtime_mode=" + hint.RuntimeMode
	}
	if hint.ProfileName != "" {
		desc += " profile=" + hint.ProfileName
	}
	return "the target workspace has no runtime with " + desc +
		"; connect this machine's daemon to the target instance (or pick another runtime) and bind again"
}

func ImportTransferConversations(ctx context.Context, env TransferImportEnv, req TransferConversationsRequest) (*TransferConversationsReport, error) {
	if req.DryRun == nil {
		return nil, &ImportError{Status: 400, Code: "transfer_bundle_invalid", Msg: "dry_run is required"}
	}
	dry := *req.DryRun
	report := &TransferConversationsReport{Applied: !dry}
	wsID := uuidString(env.TargetID)

	skipSessions := map[string]bool{}
	agentTarget := map[string]pgtype.UUID{}
	projectTarget := map[string]pgtype.UUID{}

	for _, sess := range req.Sessions {
		agentUUID, ok, unmapped := resolveTransferAgent(ctx, env, req.Refs, sess.AgentID)
		if !ok {
			skipSessions[sess.SourceID] = true
			report.SessionsSkipped++
			if unmapped {
				report.AgentUnmapped = append(report.AgentUnmapped, UnmappedRef{
					Entity: "chat_session", SourceID: sess.SourceID, Field: "agent_id",
					RefType: "agent", RefID: sess.AgentID, Resolution: "skipped_session",
				})
			}
			continue
		}
		agentTarget[sess.SourceID] = agentUUID
		if sess.ProjectID != nil && *sess.ProjectID != "" {
			pid, mapped := resolveTransferProject(ctx, env, req.Refs, *sess.ProjectID)
			if mapped {
				projectTarget[sess.SourceID] = pid
			} else {
				report.ProjectUnmapped = append(report.ProjectUnmapped, UnmappedRef{
					Entity: "chat_session", SourceID: sess.SourceID, Field: "project_id",
					RefType: "project", RefID: *sess.ProjectID, Resolution: "nulled",
				})
			}
		}
		if sess.ChannelType != nil && *sess.ChannelType != "" {
			report.ChannelDetached++
		}
		if dry {
			report.SessionsCreated++
			continue
		}
		targetID := TransferChatSessionID(wsID, sess.SourceID)
		createdAt := parseTransferTime(sess.CreatedAt)
		updatedAt := parseTransferTime(sess.UpdatedAt)
		if !updatedAt.Valid {
			updatedAt = createdAt
		}
		status := sess.Status
		if status == "" {
			status = "active"
		}
		var pinned pgtype.Timestamptz
		if sess.PinnedAt != nil && *sess.PinnedAt != "" {
			pinned = parseTransferTime(*sess.PinnedAt)
		}
		var explicit pgtype.Timestamptz
		if sess.ExplicitlyCreatedAt != nil && *sess.ExplicitlyCreatedAt != "" {
			explicit = parseTransferTime(*sess.ExplicitlyCreatedAt)
		}
		var project pgtype.UUID
		if p, ok := projectTarget[sess.SourceID]; ok {
			project = p
		}
		n, err := env.Queries.TransferInsertChatSession(ctx, db.TransferInsertChatSessionParams{
			ID:                  pgUUID(targetID),
			WorkspaceID:         env.TargetID,
			AgentID:             agentUUID,
			CreatorID:           env.ImporterID,
			Title:               sess.Title,
			Status:              status,
			PinnedAt:            pinned,
			IsAgentIntro:        sess.IsAgentIntro,
			ExplicitlyCreatedAt: explicit,
			ProjectID:           project,
			CreatedAt:           createdAt,
			UpdatedAt:           updatedAt,
		})
		if err != nil {
			return nil, fmt.Errorf("insert chat session: %w", err)
		}
		if n == 0 {
			report.SessionsSkipped++
		} else {
			report.SessionsCreated++
			// Chat pins are per person now (DENE-866): a pinned export lands
			// as the importer's own sidebar pin, not as a flag on the row.
			if pinned.Valid {
				if err := transferPinChat(ctx, env, targetID); err != nil {
					return nil, fmt.Errorf("pin chat session: %w", err)
				}
			}
		}
	}

	for _, msg := range req.Messages {
		if skipSessions[msg.ChatSessionID] {
			report.MessagesSkipped++
			continue
		}
		if _, ok := agentTarget[msg.ChatSessionID]; !ok && !dry {
			// Session may already exist from a prior import of this shard.
			targetSess := TransferChatSessionID(wsID, msg.ChatSessionID)
			if _, err := env.Queries.GetTransferChatSession(ctx, db.GetTransferChatSessionParams{
				ID: pgUUID(targetSess), WorkspaceID: env.TargetID,
			}); err != nil {
				report.MessagesSkipped++
				continue
			}
		}
		if dry {
			report.MessagesCreated++
			continue
		}
		targetMsg := TransferChatMessageID(wsID, msg.SourceID)
		targetSess := TransferChatSessionID(wsID, msg.ChatSessionID)
		kind := msg.MessageKind
		if kind == "" {
			kind = "message"
		}
		var failure pgtype.Text
		if msg.FailureReason != nil {
			failure = pgtype.Text{String: *msg.FailureReason, Valid: true}
		}
		var elapsed pgtype.Int8
		if msg.ElapsedMs != nil {
			elapsed = pgtype.Int8{Int64: *msg.ElapsedMs, Valid: true}
		}
		n, err := env.Queries.TransferInsertChatMessage(ctx, db.TransferInsertChatMessageParams{
			ID:            pgUUID(targetMsg),
			ChatSessionID: pgUUID(targetSess),
			Role:          msg.Role,
			Content:       msg.Content,
			MessageKind:   pgtype.Text{String: kind, Valid: true},
			FailureReason: failure,
			ElapsedMs:     elapsed,
			CreatedAt:     parseTransferTime(msg.CreatedAt),
		})
		if err != nil {
			return nil, fmt.Errorf("insert chat message: %w", err)
		}
		if n == 0 {
			report.MessagesSkipped++
		} else {
			report.MessagesCreated++
		}
	}
	report.Finalized = req.Finalize && !dry
	return report, nil
}

func resolveTransferAgent(ctx context.Context, env TransferImportEnv, refs TransferRefs, sourceAgentID string) (pgtype.UUID, bool, bool) {
	if ref, ok := refs.SystemAgents[sourceAgentID]; ok && ref.SystemKey != "" {
		a, err := env.Queries.GetAgentBySystemKey(ctx, db.GetAgentBySystemKeyParams{
			WorkspaceID: env.TargetID,
			SystemKey:   pgtype.Text{String: ref.SystemKey, Valid: true},
		})
		if err == nil {
			return a.ID, true, false
		}
		return pgtype.UUID{}, false, true
	}
	name := sourceAgentID
	if ref, ok := refs.Agents[sourceAgentID]; ok && ref.Name != "" {
		name = ref.Name
	}
	a, err := env.Queries.GetUserAgentByName(ctx, db.GetUserAgentByNameParams{
		WorkspaceID: env.TargetID,
		Name:        name,
	})
	if err != nil {
		return pgtype.UUID{}, false, true
	}
	return a.ID, true, false
}

func resolveTransferProject(ctx context.Context, env TransferImportEnv, refs TransferRefs, sourceProjectID string) (pgtype.UUID, bool) {
	title := sourceProjectID
	if ref, ok := refs.Projects[sourceProjectID]; ok && ref.Title != "" {
		title = ref.Title
	}
	p, err := env.Queries.GetEarliestProjectByTitle(ctx, db.GetEarliestProjectByTitleParams{
		WorkspaceID: env.TargetID,
		Title:       title,
	})
	if err != nil {
		return pgtype.UUID{}, false
	}
	return p.ID, true
}

// ---------------------------------------------------------------------------
// V3 task transfer (DENE-385)
// ---------------------------------------------------------------------------

// transferMentionRe is util.MentionRe plus the mention://project form, which
// the shared parser deliberately ignores because no server path resolves it.
// The link still has to be rewritten or dropped, or it points at a project
// that does not exist on the target.
//
// The two shapes are rewritten in ONE pass on purpose: util.MentionRe's label
// group is non-greedy but not anchored to a known kind, so leaving an
// unrecognized `](mention://project/...)` in the text lets a later match start
// at an earlier `[` and swallow every link between them.
var transferMentionRe = regexp.MustCompile(`\[@?(.+?)\]\(mention://(member|agent|squad|issue|all|project)/([0-9a-fA-F-]+|all)\)`)

// Transfer issue defaults and vocabularies. They mirror the column CHECKs so a
// malformed row degrades instead of failing the whole shard halfway through.
var (
	transferIssueStatusCategories = []string{"backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled"}
	transferIssuePriorities       = []string{"urgent", "high", "medium", "low", "none"}
	transferCommentTypes          = []string{"comment", "status_change", "progress_update", "system"}
)

// ImportTransferIssues writes one `issues` shard: issue rows, comment rows and
// the relation rows (labels, reactions) that hang off them.
//
// Every write goes through the dedicated Transfer* statements, so the import
// stays invisible to the rest of the product: no number allocation, no event,
// no task enqueue, no timestamp drift. The second pass — parent pointers,
// issue_counter watermark, creator/assignee subscribers — runs on `finalize`,
// after every shard has landed (contract §4.1, §2.2, §6.2).
func ImportTransferIssues(ctx context.Context, env TransferImportEnv, req TransferIssuesRequest) (*TransferIssuesReport, error) {
	if req.DryRun == nil {
		return nil, &ImportError{Status: 400, Code: "transfer_bundle_invalid", Msg: "dry_run is required"}
	}
	dry := *req.DryRun
	report := &TransferIssuesReport{Applied: !dry}

	if err := rejectTransferIssueSecrets(req.Issues); err != nil {
		return nil, err
	}

	foreign, err := targetWorkspaceHasForeignIssues(ctx, env, req.Refs)
	if err != nil {
		return nil, err
	}
	if foreign && !req.Renumber {
		// Keeping the source numbers is what makes every DENE-xxx text
		// reference inside bodies and comments correct on the target, so the
		// import refuses a workspace that already has tasks instead of
		// silently shifting them onto other, real issues (contract §2.2).
		// `renumber` is the caller saying it did that shift on purpose (§2.3).
		return nil, &ImportError{
			Status: 400,
			Code:   "transfer_issues_target_not_empty",
			Msg:    "the target workspace already has issues; import tasks into an empty workspace",
		}
	}

	limit, err := checkTransferIssueLimit(ctx, env, req, report)
	if err != nil {
		return nil, err
	}
	report.IssueLimit = limit

	state := &transferIssueState{
		env:         env,
		wsID:        uuidString(env.TargetID),
		refs:        req.Refs,
		report:      report,
		people:      map[string]pgtype.UUID{},
		agents:      map[string]pgtype.UUID{},
		squads:      map[string]pgtype.UUID{},
		projects:    map[string]pgtype.UUID{},
		properties:  map[string]db.IssueProperty{},
		labelCache:  map[string]*pgtype.UUID{},
		statusCache: map[string]*string{},
	}
	if err := state.resolveRefs(ctx); err != nil {
		return nil, err
	}
	plan, err := state.plan(ctx, req)
	if err != nil {
		return nil, err
	}
	if dry {
		report.IssuesCreated = countPlannedIssues(plan.issues)
		report.CommentsCreated = countPlannedComments(plan.comments)
		report.LabelsCreated = len(plan.labels)
		report.ReactionsCreated = len(plan.issueReactions) + len(plan.commentReactions)
		report.Finalized = req.Finalize
		return report, nil
	}
	// The whole write half is one transaction, so a shard that fails halfway
	// through rolls back instead of leaving the target holding half a task tree
	// (contract §7.1). The reads above stay outside it: they only resolve
	// identities and downgrades into the plan.
	tx, err := env.TxStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin issues tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	writeState := *state
	writeState.env.Queries = env.Queries.WithTx(tx)
	if err := writeState.write(ctx, plan); err != nil {
		return nil, err
	}
	if req.Finalize {
		if err := writeState.finalize(ctx, plan); err != nil {
			return nil, err
		}
		report.Finalized = true
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit issues: %w", err)
	}
	return report, nil
}

// checkTransferIssueLimit echoes the workspace's resolved issue quota policy
// and refuses the whole shard when an enforced limit would be crossed.
//
// The import deliberately does not call CheckIssueCreateCapacity: that is a
// commercial gate, not a data-integrity one, and the transfer path never
// allocates a number. Not crossing an enforced limit silently is still
// required, so the policy is read before the first write (contract §11.1).
func checkTransferIssueLimit(ctx context.Context, env TransferImportEnv, req TransferIssuesRequest, report *TransferIssuesReport) (*TransferIssueLimitPolicy, error) {
	policy := ResolveIssueCountPolicy(ctx, env.Entitlements, env.TargetID)
	out := &TransferIssueLimitPolicy{Action: string(policy.Action)}
	if policy.Action != entitlement.ActionEnforce {
		return out, nil
	}
	used, err := CountIssueUsage(ctx, env.Queries, env.TargetID, policy)
	if err != nil {
		return nil, fmt.Errorf("count issue usage: %w", err)
	}
	out.Limit = policy.Limit
	out.Used = used
	incoming, err := countIncomingTransferIssues(ctx, env, req.Issues)
	if err != nil {
		return nil, err
	}
	if used+incoming > policy.Limit {
		report.IssueLimit = out
		return nil, &ImportError{
			Status: 400,
			Code:   "issue_limit_would_exceed",
			Msg:    "the target workspace's issue quota would be exceeded",
			Report: report,
		}
	}
	return out, nil
}

// targetWorkspaceHasForeignIssues is the §2.2 "target must be empty" gate.
//
// A plain "the workspace has no issues" test cannot work: a package that spans
// several shards necessarily holds issues after the first one. What the gate
// really protects is that the workspace held no *other* tasks, so the bundle's
// own numbering and prefix stay authoritative. A row this import wrote is
// recognized by its deterministic id against the package-wide refs.issues
// index that ships with every shard.
//
// The caller, not this function, decides what to do with the answer: a foreign
// row is a 400 unless the request carries `renumber`, which is the caller
// stating it offset every number above the target's watermark (§2.3).
func targetWorkspaceHasForeignIssues(ctx context.Context, env TransferImportEnv, refs TransferRefs) (bool, error) {
	ids, err := env.Queries.TransferListIssueIDs(ctx, env.TargetID)
	if err != nil {
		return false, fmt.Errorf("list target issues: %w", err)
	}
	if len(ids) == 0 {
		return false, nil
	}
	wsID := uuidString(env.TargetID)
	imported := make(map[string]struct{}, len(refs.Issues))
	for sourceID := range refs.Issues {
		imported[TransferIssueID(wsID, sourceID).String()] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := imported[uuidString(id)]; !ok {
			return true, nil
		}
	}
	return false, nil
}

// countIncomingTransferIssues counts the rows this request would really add, so
// a finalize request that re-sends rows already written does not double-count.
func countIncomingTransferIssues(ctx context.Context, env TransferImportEnv, rows []TransferIssueRow) (int64, error) {
	var incoming int64
	for _, row := range rows {
		if row.SourceID == "" {
			incoming++
			continue
		}
		issue, err := env.Queries.GetIssue(ctx, pgUUID(TransferIssueID(uuidString(env.TargetID), row.SourceID)))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				incoming++
				continue
			}
			return 0, fmt.Errorf("count incoming issues: %w", err)
		}
		if uuidString(issue.WorkspaceID) != uuidString(env.TargetID) {
			incoming++
		}
	}
	return incoming, nil
}

// issueExists reports whether an imported issue already landed, which is how a
// finalize request tells a link-only pair from a missing row.
func (s *transferIssueState) issueExists(ctx context.Context, id pgtype.UUID) (bool, error) {
	issue, err := s.env.Queries.GetIssue(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("resolve issue: %w", err)
	}
	return uuidString(issue.WorkspaceID) == s.wsID, nil
}

// transferIssueState holds the resolved identity maps one shard needs. `refs`
// travels with every shard, so nothing has to be remembered between requests.
type transferIssueState struct {
	env      TransferImportEnv
	wsID     string
	refs     TransferRefs
	report   *TransferIssuesReport
	people   map[string]pgtype.UUID
	agents   map[string]pgtype.UUID
	squads   map[string]pgtype.UUID
	projects map[string]pgtype.UUID
	// properties maps a source property definition id to the target definition
	// the config import created for it (same name, fresh id).
	properties   map[string]db.IssueProperty
	labelCache   map[string]*pgtype.UUID
	statusCache  map[string]*string
	propertyMiss map[string]bool
}

// resolveRefs resolves the whole reference index once per shard. The maps are
// workspace-sized (members, agents, squads, projects, property definitions),
// so resolving them eagerly keeps the write loop free of error handling.
func (s *transferIssueState) resolveRefs(ctx context.Context) error {
	for sourceID, ref := range s.refs.Members {
		email := strings.TrimSpace(ref.Email)
		if email == "" {
			continue
		}
		m, err := s.env.Queries.GetWorkspaceMemberByEmail(ctx, db.GetWorkspaceMemberByEmailParams{
			WorkspaceID: s.env.TargetID,
			Email:       email,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return fmt.Errorf("resolve member %s: %w", sourceID, err)
		}
		s.people[sourceID] = m.UserID
	}
	for sourceID := range s.refs.SystemAgents {
		if a, ok, _ := resolveTransferAgent(ctx, s.env, s.refs, sourceID); ok {
			s.agents[sourceID] = a
		}
	}
	for sourceID := range s.refs.Agents {
		if _, isSystem := s.refs.SystemAgents[sourceID]; isSystem {
			continue
		}
		if a, ok, _ := resolveTransferAgent(ctx, s.env, s.refs, sourceID); ok {
			s.agents[sourceID] = a
		}
	}
	for sourceID, ref := range s.refs.Squads {
		if strings.TrimSpace(ref.Name) == "" {
			continue
		}
		sq, err := s.env.Queries.GetEarliestSquadByName(ctx, db.GetEarliestSquadByNameParams{
			WorkspaceID: s.env.TargetID,
			Name:        ref.Name,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return fmt.Errorf("resolve squad %s: %w", sourceID, err)
		}
		s.squads[sourceID] = sq.ID
	}
	for sourceID, ref := range s.refs.Projects {
		if strings.TrimSpace(ref.Title) == "" {
			continue
		}
		p, err := s.env.Queries.GetEarliestProjectByTitle(ctx, db.GetEarliestProjectByTitleParams{
			WorkspaceID: s.env.TargetID,
			Title:       ref.Title,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return fmt.Errorf("resolve project %s: %w", sourceID, err)
		}
		s.projects[sourceID] = p.ID
	}
	for sourceID, ref := range s.refs.IssueProperties {
		name := strings.TrimSpace(ref.Name)
		if name == "" {
			continue
		}
		def, ok, err := s.propertyByName(ctx, name)
		if err != nil {
			return err
		}
		if ok {
			s.properties[sourceID] = def
		}
	}
	return nil
}

func (s *transferIssueState) propertyByName(ctx context.Context, name string) (db.IssueProperty, bool, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	def, err := s.env.Queries.GetIssuePropertyByName(ctx, db.GetIssuePropertyByNameParams{
		WorkspaceID: s.env.TargetID,
		Lower:       key,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.IssueProperty{}, false, nil
		}
		return db.IssueProperty{}, false, fmt.Errorf("resolve property %q: %w", name, err)
	}
	return def, true, nil
}

// targetStatusKey resolves a status key against the target catalog and degrades
// to a category the catalog definitely has (contract §1.4). Writing a key the
// catalog does not know would make the issue disappear from every
// status-grouped view.
//
// A key the catalog DOES know is written through unchanged. Returning its
// category instead would look correct for the seven built-ins — migration 339
// seeds them with category == key — and silently flatten every custom status
// onto its category for everyone else.
func (s *transferIssueState) targetStatusKey(ctx context.Context, row TransferIssueRow) (string, bool, error) {
	key := strings.TrimSpace(row.Status)
	if key == "" {
		key = "backlog"
	}
	if _, ok, err := s.statusCategory(ctx, key); err != nil {
		return "", false, err
	} else if ok {
		return key, false, nil
	}
	// Unknown key: fall back to the category the source recorded, then to
	// backlog if even that is not in the target catalog.
	for _, candidate := range []string{row.StatusCategory, s.refs.IssueStatuses[row.Status], "backlog"} {
		if candidate == "" {
			continue
		}
		if _, ok, err := s.statusCategory(ctx, candidate); err != nil {
			return "", false, err
		} else if ok {
			return candidate, true, nil
		}
	}
	return "backlog", true, nil
}

// statusCategory reports whether a status key exists in the target workspace
// and returns its category. The result is cached per shard; a nil entry means
// "absent".
func (s *transferIssueState) statusCategory(ctx context.Context, key string) (string, bool, error) {
	if cached, ok := s.statusCache[key]; ok {
		if cached == nil {
			return "", false, nil
		}
		return *cached, true, nil
	}
	entry, err := s.env.Queries.GetIssueStatusEntryByKey(ctx, db.GetIssueStatusEntryByKeyParams{
		WorkspaceID: s.env.TargetID,
		Key:         key,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.statusCache[key] = nil
			return "", false, nil
		}
		return "", false, fmt.Errorf("resolve issue status %q: %w", key, err)
	}
	category := entry.Category
	s.statusCache[key] = &category
	return category, true, nil
}

func (s *transferIssueState) labelTarget(ctx context.Context, resourceType, name string) (pgtype.UUID, bool, error) {
	resourceType = strings.TrimSpace(resourceType)
	name = strings.TrimSpace(name)
	if resourceType == "" || name == "" {
		return pgtype.UUID{}, false, nil
	}
	key := resourceType + "\x00" + strings.ToLower(name)
	if cached, ok := s.labelCache[key]; ok {
		if cached == nil {
			return pgtype.UUID{}, false, nil
		}
		return *cached, true, nil
	}
	label, err := s.env.Queries.GetLabelByIdentity(ctx, db.GetLabelByIdentityParams{
		WorkspaceID:  s.env.TargetID,
		ResourceType: resourceType,
		Lower:        name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			s.labelCache[key] = nil
			return pgtype.UUID{}, false, nil
		}
		return pgtype.UUID{}, false, fmt.Errorf("resolve label %q: %w", name, err)
	}
	s.labelCache[key] = &label.ID
	return label.ID, true, nil
}

// ---------------------------------------------------------------------------
// Planning: everything is validated and mapped before the first write, so a
// malformed row rejects the request instead of leaving half a shard behind.
// ---------------------------------------------------------------------------

type transferPlannedIssue struct {
	sourceID     string
	parentSource string
	targetID     pgtype.UUID
	// linkOnly marks a row a finalize request carries as nothing but a
	// (source_id, source_parent_id) pair for an issue an earlier shard already
	// wrote: the whole package's link pairs fit in one request, its full rows
	// do not.
	linkOnly     bool
	params       db.TransferInsertIssueParams
	creatorType  string
	creatorID    pgtype.UUID
	assigneeType string
	assigneeID   pgtype.UUID
	createdAt    pgtype.Timestamptz
}

type transferPlannedComment struct {
	sourceID     string
	parentSource string
	issueSource  string
	targetID     pgtype.UUID
	// linkOnly: see transferPlannedIssue. A comment link row needs only
	// source_id, issue_id and parent_id.
	linkOnly bool
	params   db.TransferInsertCommentParams
}

type transferPlannedLabel struct {
	issueSourceID string
	labelID       pgtype.UUID
}

type transferIssuePlan struct {
	issues           []transferPlannedIssue
	comments         []transferPlannedComment
	labels           []transferPlannedLabel
	issueReactions   []db.TransferInsertIssueReactionParams
	commentReactions []db.TransferInsertCommentReactionParams
}

func (s *transferIssueState) plan(ctx context.Context, req TransferIssuesRequest) (*transferIssuePlan, error) {
	plan := &transferIssuePlan{}
	for _, row := range req.Issues {
		planned, err := s.planIssue(ctx, row)
		if err != nil {
			return nil, err
		}
		plan.issues = append(plan.issues, planned)
	}
	for _, row := range req.Comments {
		planned, err := s.planComment(ctx, row)
		if err != nil {
			return nil, err
		}
		plan.comments = append(plan.comments, planned)
	}
	for _, row := range req.Relations {
		if err := s.planRelation(ctx, row, plan); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

func (s *transferIssueState) planIssue(ctx context.Context, row TransferIssueRow) (transferPlannedIssue, error) {
	out := transferPlannedIssue{sourceID: row.SourceID, parentSource: derefString(row.ParentIssueID)}
	if strings.TrimSpace(row.SourceID) == "" {
		return out, transferIssuesInvalid("issue.source_id is required")
	}
	out.targetID = pgUUID(TransferIssueID(s.wsID, row.SourceID))

	if strings.TrimSpace(row.CreatedAt) == "" {
		exists, err := s.issueExists(ctx, out.targetID)
		if err != nil {
			return out, err
		}
		if !exists {
			return out, transferIssuesInvalid("issue " + row.SourceID + " is missing its required fields")
		}
		out.linkOnly = true
		return out, nil
	}

	createdAt, err := transferTime(row.CreatedAt)
	if err != nil {
		return out, transferIssuesInvalid("issue " + row.SourceID + " created_at: " + err.Error())
	}
	updatedAt, err := transferTime(row.UpdatedAt)
	if err != nil {
		return out, transferIssuesInvalid("issue " + row.SourceID + " updated_at: " + err.Error())
	}
	lastActivityAt := updatedAt
	if strings.TrimSpace(row.LastActivityAt) != "" {
		lastActivityAt, err = transferTime(row.LastActivityAt)
		if err != nil {
			return out, transferIssuesInvalid("issue " + row.SourceID + " last_activity_at: " + err.Error())
		}
	}

	status, downgraded, err := s.targetStatusKey(ctx, row)
	if err != nil {
		return out, err
	}
	if downgraded {
		s.report.StatusUnmapped = append(s.report.StatusUnmapped, UnmappedRef{
			Entity: "issue", SourceID: row.SourceID, Field: "status",
			RefType: "issue_status", RefID: row.Status,
			Reason: "status_key_unmapped", Resolution: "category",
		})
	}

	creatorType, creatorID := s.resolveCreator(ctx, row)
	assigneeType, assigneeID := s.resolveAssignee(ctx, row)

	var projectID pgtype.UUID
	if source := derefString(row.ProjectID); source != "" {
		if p, ok := s.projects[source]; ok {
			projectID = p
		} else {
			s.report.ProjectUnmapped = append(s.report.ProjectUnmapped, UnmappedRef{
				Entity: "issue", SourceID: row.SourceID, Field: "project_id",
				RefType: "project", RefID: source,
				Reason: "project_unmapped", Resolution: "nulled",
			})
		}
	}

	description := row.Description
	if description != nil {
		rewritten := s.rewriteMentions("issue", row.SourceID, *description)
		description = &rewritten
	}
	metadata, err := transferJSONObject(row.Metadata)
	if err != nil {
		return out, transferIssuesInvalid("issue " + row.SourceID + " metadata: " + err.Error())
	}
	properties, err := s.remapProperties(ctx, row)
	if err != nil {
		return out, err
	}

	out.params = db.TransferInsertIssueParams{
		ID:             out.targetID,
		WorkspaceID:    s.env.TargetID,
		Number:         row.Number,
		Title:          row.Title,
		Status:         status,
		Priority:       transferEnum(row.Priority, transferIssuePriorities, "none"),
		CreatorType:    creatorType,
		CreatorID:      creatorID,
		Position:       row.Position,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		LastActivityAt: lastActivityAt,
		Description:    transferText(description),
		AssigneeType:   transferText(optionalString(assigneeType)),
		AssigneeID:     assigneeID,
		ProjectID:      projectID,
		Stage:          transferInt4(row.Stage),
		StartDate:      transferDate(row.StartDate),
		DueDate:        transferDate(row.DueDate),
		Metadata:       metadata,
		Properties:     properties,
	}
	out.creatorType = creatorType
	out.creatorID = creatorID
	out.assigneeType = assigneeType
	out.assigneeID = assigneeID
	out.createdAt = createdAt
	return out, nil
}

func (s *transferIssueState) planComment(ctx context.Context, row TransferCommentRow) (transferPlannedComment, error) {
	out := transferPlannedComment{sourceID: row.SourceID}
	if strings.TrimSpace(row.SourceID) == "" {
		return out, transferIssuesInvalid("comment.source_id is required")
	}
	if strings.TrimSpace(row.IssueID) == "" {
		return out, transferIssuesInvalid("comment " + row.SourceID + " issue_id is required")
	}
	out.targetID = pgUUID(TransferCommentID(s.wsID, row.SourceID))
	out.issueSource = row.IssueID
	out.parentSource = derefString(row.ParentID)

	if strings.TrimSpace(row.CreatedAt) == "" {
		if _, err := s.env.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
			ID:          out.targetID,
			WorkspaceID: s.env.TargetID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return out, transferIssuesInvalid("comment " + row.SourceID + " is missing its required fields")
			}
			return out, fmt.Errorf("resolve comment: %w", err)
		}
		out.linkOnly = true
		return out, nil
	}

	createdAt, err := transferTime(row.CreatedAt)
	if err != nil {
		return out, transferIssuesInvalid("comment " + row.SourceID + " created_at: " + err.Error())
	}
	updatedAt, err := transferTime(row.UpdatedAt)
	if err != nil {
		return out, transferIssuesInvalid("comment " + row.SourceID + " updated_at: " + err.Error())
	}
	var deletedAt pgtype.Timestamptz
	if strings.TrimSpace(derefString(row.DeletedAt)) != "" {
		deletedAt, err = transferTime(derefString(row.DeletedAt))
		if err != nil {
			return out, transferIssuesInvalid("comment " + row.SourceID + " deleted_at: " + err.Error())
		}
	}

	authorType, authorID := s.resolveCommentAuthor(ctx, row)
	resolvedAt, resolvedByType, resolvedByID, err := s.resolveResolution(ctx, row)
	if err != nil {
		return out, err
	}

	out.params = db.TransferInsertCommentParams{
		ID:             out.targetID,
		IssueID:        pgUUID(TransferIssueID(s.wsID, row.IssueID)),
		WorkspaceID:    s.env.TargetID,
		AuthorType:     authorType,
		AuthorID:       authorID,
		Content:        s.rewriteMentions("comment", row.SourceID, row.Content),
		Type:           transferEnum(row.Type, transferCommentTypes, "comment"),
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		ResolvedAt:     resolvedAt,
		ResolvedByType: resolvedByType,
		ResolvedByID:   resolvedByID,
		DeletedAt:      deletedAt,
	}
	return out, nil
}

func (s *transferIssueState) planRelation(ctx context.Context, row TransferRelationRow, plan *transferIssuePlan) error {
	switch row.Kind {
	case TransferRelationIssueLabel:
		if strings.TrimSpace(row.IssueID) == "" {
			return transferIssuesInvalid("issue_label relation is missing issue_id")
		}
		labelID, ok, err := s.labelTarget(ctx, row.LabelResourceType, row.LabelName)
		if err != nil {
			return err
		}
		if !ok {
			// The label definition never made it to the target (its config
			// group was skipped or the label was deleted); the relation is
			// dropped rather than attached to a label that does not exist.
			s.report.LabelUnmapped = append(s.report.LabelUnmapped, UnmappedRef{
				Entity: "issue", SourceID: row.IssueID, Field: "label_id",
				RefType: row.LabelResourceType, RefID: row.LabelName,
				Reason: "label_unmapped", Resolution: "dropped",
			})
			return nil
		}
		plan.labels = append(plan.labels, transferPlannedLabel{issueSourceID: row.IssueID, labelID: labelID})

	case TransferRelationIssueReaction, TransferRelationCommentReaction:
		isComment := row.Kind == TransferRelationCommentReaction
		refSource := row.IssueID
		if isComment {
			refSource = row.CommentID
		}
		if strings.TrimSpace(refSource) == "" {
			return transferIssuesInvalid(row.Kind + " relation is missing its target id")
		}
		actorType, actorID, ok, err := s.resolveActor(row.ActorType, row.ActorID)
		if err != nil {
			return err
		}
		if !ok {
			// A reaction on the wrong person's behalf is worse than a missing
			// one, so an unmapped actor drops the row (contract §1.2).
			s.report.ReactionUnmapped = append(s.report.ReactionUnmapped, UnmappedRef{
				Entity: strings.TrimPrefix(row.Kind, "issue_"), SourceID: refSource, Field: "actor_id",
				RefType: row.ActorType, RefID: row.ActorID,
				Reason: "reaction_actor_unmapped", Resolution: "dropped",
			})
			return nil
		}
		createdAt, err := transferTime(row.CreatedAt)
		if err != nil {
			return transferIssuesInvalid(row.Kind + " " + refSource + " created_at: " + err.Error())
		}
		// The relation row carries no reaction id, so the deterministic id is
		// derived from the table's natural key. That keeps a re-import on the
		// same target row instead of tripping the (target, actor, emoji)
		// unique index.
		reactionID := pgUUID(TransferReactionID(s.wsID, row.Kind+"/"+refSource+"/"+actorType+":"+row.ActorID+"/"+row.Emoji))
		if isComment {
			plan.commentReactions = append(plan.commentReactions, db.TransferInsertCommentReactionParams{
				ID:          reactionID,
				CommentID:   pgUUID(TransferCommentID(s.wsID, row.CommentID)),
				WorkspaceID: s.env.TargetID,
				ActorType:   actorType,
				ActorID:     actorID,
				Emoji:       row.Emoji,
				CreatedAt:   createdAt,
			})
			return nil
		}
		plan.issueReactions = append(plan.issueReactions, db.TransferInsertIssueReactionParams{
			ID:          reactionID,
			IssueID:     pgUUID(TransferIssueID(s.wsID, row.IssueID)),
			WorkspaceID: s.env.TargetID,
			ActorType:   actorType,
			ActorID:     actorID,
			Emoji:       row.Emoji,
			CreatedAt:   createdAt,
		})

	default:
		return transferIssuesInvalid("unknown relation kind: " + row.Kind)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Identity mapping
// ---------------------------------------------------------------------------

// resolveActor maps one polymorphic (type, id) reference. `ok == false` means
// the caller applies its own documented degradation: the importer for an
// assignee, creator or author, dropped for a reaction.
func (s *transferIssueState) resolveActor(actorType, actorID string) (string, pgtype.UUID, bool, error) {
	switch actorType {
	case "member":
		if id, ok := s.people[actorID]; ok {
			return "member", id, true, nil
		}
	case "agent":
		if id, ok := s.agents[actorID]; ok {
			return "agent", id, true, nil
		}
	case "squad":
		if id, ok := s.squads[actorID]; ok {
			return "squad", id, true, nil
		}
	case "system":
		return "system", transferUUIDOrNil(actorID), true, nil
	}
	return actorType, pgtype.UUID{}, false, nil
}

func (s *transferIssueState) resolveCreator(ctx context.Context, row TransferIssueRow) (string, pgtype.UUID) {
	actorType, actorID, ok, _ := s.resolveActor(row.CreatorType, row.CreatorID)
	if ok && (actorType == "member" || actorType == "agent") {
		return actorType, actorID
	}
	// creator_* is NOT NULL, so the fallback is the importer rather than an
	// empty value (contract §5.1).
	s.report.CreatorUnmapped = append(s.report.CreatorUnmapped, UnmappedRef{
		Entity: "issue", SourceID: row.SourceID, Field: "creator_id",
		RefType: row.CreatorType, RefID: row.CreatorID,
		Reason: "creator_unmapped", Resolution: "importer",
	})
	return "member", s.env.ImporterID
}

func (s *transferIssueState) resolveAssignee(ctx context.Context, row TransferIssueRow) (string, pgtype.UUID) {
	source := derefString(row.AssigneeType)
	if source == "" {
		return "", pgtype.UUID{}
	}
	actorType, actorID, ok, _ := s.resolveActor(source, derefString(row.AssigneeID))
	if !ok {
		// An unmappable assignee falls back to the importer, matching creator
		// and comment author: a workspace owner who migrates their own history
		// would rather inherit the orphans than hunt for silently empty rows.
		// The degradation is still reported; only its resolution changed.
		s.report.AssigneeUnmapped = append(s.report.AssigneeUnmapped, UnmappedRef{
			Entity: "issue", SourceID: row.SourceID, Field: "assignee_id",
			RefType: source, RefID: derefString(row.AssigneeID),
			Reason: "assignee_unmapped", Resolution: "importer",
		})
		return "member", s.env.ImporterID
	}
	return actorType, actorID
}

func (s *transferIssueState) resolveCommentAuthor(ctx context.Context, row TransferCommentRow) (string, pgtype.UUID) {
	// A `system` author is the platform narrating itself; it carries no target
	// identity to resolve and is written through unchanged (contract §1.5).
	if row.AuthorType == "system" {
		return "system", transferUUIDOrNil(row.AuthorID)
	}
	actorType, actorID, ok, _ := s.resolveActor(row.AuthorType, row.AuthorID)
	if ok {
		return actorType, actorID
	}
	s.report.AuthorUnmapped = append(s.report.AuthorUnmapped, UnmappedRef{
		Entity: "comment", SourceID: row.SourceID, Field: "author_id",
		RefType: row.AuthorType, RefID: row.AuthorID,
		Reason: "comment_author_unmapped", Resolution: "importer",
	})
	return "member", s.env.ImporterID
}

// resolveResolution maps the resolved_* triple. The three columns are
// constrained to live or die together, so an unmappable actor clears all three
// rather than only the id (contract §5.1).
func (s *transferIssueState) resolveResolution(ctx context.Context, row TransferCommentRow) (pgtype.Timestamptz, pgtype.Text, pgtype.UUID, error) {
	if strings.TrimSpace(derefString(row.ResolvedAt)) == "" {
		return pgtype.Timestamptz{}, pgtype.Text{}, pgtype.UUID{}, nil
	}
	resolvedAt, err := transferTime(derefString(row.ResolvedAt))
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.Text{}, pgtype.UUID{}, transferIssuesInvalid("comment " + row.SourceID + " resolved_at: " + err.Error())
	}
	actorType := derefString(row.ResolvedByType)
	mappedType, actorID, ok, err := s.resolveActor(actorType, derefString(row.ResolvedByID))
	if err != nil {
		return pgtype.Timestamptz{}, pgtype.Text{}, pgtype.UUID{}, err
	}
	if ok {
		actorType = mappedType
	}
	if !ok {
		s.report.ResolutionUnmapped = append(s.report.ResolutionUnmapped, UnmappedRef{
			Entity: "comment", SourceID: row.SourceID, Field: "resolved_by_id",
			RefType: actorType, RefID: derefString(row.ResolvedByID),
			Reason: "resolution_actor_unmapped", Resolution: "nulled",
		})
		return pgtype.Timestamptz{}, pgtype.Text{}, pgtype.UUID{}, nil
	}
	return resolvedAt, pgtype.Text{String: actorType, Valid: true}, actorID, nil
}

// ---------------------------------------------------------------------------
// issue.properties
// ---------------------------------------------------------------------------

// remapProperties rebuilds the property value bag: the top-level keys are the
// source workspace's property definition ids, which the config import replaced
// with fresh ones on the target, and `actor` / `multi_actor` values embed a
// member/agent uuid that has to move with them (contract §1.2.1).
func (s *transferIssueState) remapProperties(ctx context.Context, row TransferIssueRow) ([]byte, error) {
	raw, err := transferJSONObject(row.Properties)
	if err != nil {
		return nil, transferIssuesInvalid("issue " + row.SourceID + " properties: " + err.Error())
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var bag map[string]json.RawMessage
	if err := json.Unmarshal(raw, &bag); err != nil {
		return nil, transferIssuesInvalid("issue " + row.SourceID + " properties: must be a JSON object")
	}
	out := map[string]json.RawMessage{}
	for sourcePropertyID, value := range bag {
		def, ok := s.properties[sourcePropertyID]
		if !ok {
			s.report.PropertyUnmapped = append(s.report.PropertyUnmapped, UnmappedRef{
				Entity: "issue", SourceID: row.SourceID, Field: "properties",
				RefType: "issue_property", RefID: sourcePropertyID,
				Reason: "property_unmapped", Resolution: "dropped",
			})
			continue
		}
		if def.Type == "actor" || def.Type == "multi_actor" {
			remapped, changed, keep, err := s.remapPropertyActors(row, def.Type, value)
			if err != nil {
				return nil, err
			}
			if !keep {
				continue
			}
			if !changed {
				out[uuidString(def.ID)] = value
				continue
			}
			value = remapped
		}
		out[uuidString(def.ID)] = value
	}
	return json.Marshal(out)
}

// remapPropertyActors rewrites the actor references inside one property value.
// `keep == false` means every reference was unmappable and the key is dropped.
func (s *transferIssueState) remapPropertyActors(row TransferIssueRow, propertyType string, value json.RawMessage) (json.RawMessage, bool, bool, error) {
	if propertyType == "actor" {
		remapped, keep := s.remapActorString(row, stringFromJSON(value))
		if !keep {
			return nil, false, false, nil
		}
		return mustJSON(remapped), remapped != stringFromJSON(value), true, nil
	}
	var items []string
	if err := json.Unmarshal(value, &items); err != nil {
		// A malformed value is dropped rather than aborting the shard: the
		// property definition is cosmetic next to the issue itself.
		return nil, false, false, nil
	}
	out := make([]string, 0, len(items))
	changed := false
	for _, item := range items {
		remapped, keep := s.remapActorString(row, item)
		if !keep {
			changed = true
			continue
		}
		if remapped != item {
			changed = true
		}
		out = append(out, remapped)
	}
	if len(out) == 0 {
		return nil, false, false, nil
	}
	return mustJSON(out), changed, true, nil
}

func (s *transferIssueState) remapActorString(row TransferIssueRow, raw string) (string, bool) {
	kind, id, found := strings.Cut(raw, ":")
	if !found {
		return raw, true
	}
	switch kind {
	case "member", "agent", "squad":
	default:
		// An unknown kind predates this importer; leave it exactly as it is.
		return raw, true
	}
	if mappedType, mapped, ok, _ := s.resolveActor(kind, id); ok {
		// The stored form is "<kind>:<uuid>"; the kind rides along so a
		// reference keeps saying who it points at.
		return mappedType + ":" + uuidString(mapped), true
	}
	s.report.PropertyUnmapped = append(s.report.PropertyUnmapped, UnmappedRef{
		Entity: "issue", SourceID: row.SourceID, Field: "properties",
		RefType: kind, RefID: id,
		Reason: "property_actor_unmapped", Resolution: "dropped_value",
	})
	return "", false
}

// ---------------------------------------------------------------------------
// Mentions
// ---------------------------------------------------------------------------

// rewriteMentions rewrites every mention link whose target can be resolved on
// the target workspace and degrades the rest to plain text.
//
// Keeping a dead link is not a cosmetic loss: an unresolvable
// mention://agent/... in a thread root makes routeConversationOwnersForRoot
// answer "handled, no trigger", so replies in that thread silently stop
// waking anyone (contract §3.3).
func (s *transferIssueState) rewriteMentions(entity, sourceID, text string) string {
	if text == "" || !strings.Contains(text, "mention://") {
		return text
	}
	return transferMentionRe.ReplaceAllStringFunc(text, func(match string) string {
		sub := transferMentionRe.FindStringSubmatch(match)
		if len(sub) < 4 {
			return match
		}
		label, kind, refID := sub[1], sub[2], sub[3]
		if kind == "all" {
			return match
		}
		target, ok := s.resolveMentionTarget(kind, refID)
		if !ok {
			s.recordMentionDegrade(entity, sourceID, kind, refID)
			return mentionAtPrefix(match) + label
		}
		return "[" + mentionAtPrefix(match) + label + "](mention://" + kind + "/" + target + ")"
	})
}

func (s *transferIssueState) resolveMentionTarget(kind, refID string) (string, bool) {
	switch kind {
	case "member":
		if id, ok := s.people[refID]; ok {
			return uuidString(id), true
		}
	case "agent":
		if id, ok := s.agents[refID]; ok {
			return uuidString(id), true
		}
	case "squad":
		if id, ok := s.squads[refID]; ok {
			return uuidString(id), true
		}
	case "issue":
		// An issue mention resolves only when the issue itself is in the
		// bundle: the deterministic id of an issue that was never exported
		// points at nothing.
		if _, inBundle := s.refs.Issues[refID]; inBundle {
			return TransferIssueID(s.wsID, refID).String(), true
		}
	case "project":
		if id, ok := s.projects[refID]; ok {
			return uuidString(id), true
		}
	}
	return "", false
}

func (s *transferIssueState) recordMentionDegrade(entity, sourceID, kind, refID string) {
	s.report.MentionUnmapped = append(s.report.MentionUnmapped, UnmappedRef{
		Entity: entity, SourceID: sourceID, Field: "mention",
		RefType: kind, RefID: refID,
		Reason: "mention_unmapped", Resolution: "plain_text",
	})
	if s.report.MentionUnmappedByType == nil {
		s.report.MentionUnmappedByType = map[string]int{}
	}
	s.report.MentionUnmappedByType[kind]++
}

func mentionAtPrefix(match string) string {
	if strings.HasPrefix(match, "[@") {
		return "@"
	}
	return ""
}

// ---------------------------------------------------------------------------
// Writes and finalize
// ---------------------------------------------------------------------------

// countPlannedIssues / countPlannedComments report what an apply would write,
// leaving out the link-only pairs a finalize request may carry.
func countPlannedIssues(rows []transferPlannedIssue) int {
	n := 0
	for _, row := range rows {
		if !row.linkOnly {
			n++
		}
	}
	return n
}

func countPlannedComments(rows []transferPlannedComment) int {
	n := 0
	for _, row := range rows {
		if !row.linkOnly {
			n++
		}
	}
	return n
}

func (s *transferIssueState) write(ctx context.Context, plan *transferIssuePlan) error {
	for i := range plan.issues {
		if plan.issues[i].linkOnly {
			continue
		}
		n, err := s.env.Queries.TransferInsertIssue(ctx, plan.issues[i].params)
		if err != nil {
			return fmt.Errorf("insert issue: %w", err)
		}
		if n == 0 {
			s.report.IssuesSkipped++
		} else {
			s.report.IssuesCreated++
		}
		// The creator / assignee subscriptions are written here, with the row
		// they belong to, and re-run (idempotently) at finalize. Rebuilding
		// them only at finalize would make a link-only finalize payload — the
		// shape that keeps a whole package's links inside the body cap —
		// unable to rebuild anything.
		if err := s.rebuildSubscribers(ctx, []transferPlannedIssue{plan.issues[i]}); err != nil {
			return err
		}
	}
	for i := range plan.comments {
		if plan.comments[i].linkOnly {
			continue
		}
		n, err := s.env.Queries.TransferInsertComment(ctx, plan.comments[i].params)
		if err != nil {
			return fmt.Errorf("insert comment: %w", err)
		}
		if n == 0 {
			s.report.CommentsSkipped++
		} else {
			s.report.CommentsCreated++
		}
	}
	for _, label := range plan.labels {
		n, err := s.env.Queries.TransferInsertIssueLabel(ctx, db.TransferInsertIssueLabelParams{
			IssueID: pgUUID(TransferIssueID(s.wsID, label.issueSourceID)),
			LabelID: label.labelID,
		})
		if err != nil {
			return fmt.Errorf("insert issue label: %w", err)
		}
		if n == 0 {
			s.report.LabelsSkipped++
		} else {
			s.report.LabelsCreated++
		}
	}
	for _, reaction := range plan.issueReactions {
		n, err := s.env.Queries.TransferInsertIssueReaction(ctx, reaction)
		if err != nil {
			return fmt.Errorf("insert issue reaction: %w", err)
		}
		if n == 0 {
			s.report.ReactionsSkipped++
		} else {
			s.report.ReactionsCreated++
		}
	}
	for _, reaction := range plan.commentReactions {
		n, err := s.env.Queries.TransferInsertCommentReaction(ctx, reaction)
		if err != nil {
			return fmt.Errorf("insert comment reaction: %w", err)
		}
		if n == 0 {
			s.report.ReactionsSkipped++
		} else {
			s.report.ReactionsCreated++
		}
	}
	return nil
}

// finalize runs the second pass: parent pointers, the issue-number watermark,
// and the creator/assignee subscriber rebuild. It is idempotent, so
// re-importing the same bundle leaves every value (and every row count) alone.
func (s *transferIssueState) finalize(ctx context.Context, plan *transferIssuePlan) error {
	if err := s.backfillIssueParents(ctx, plan); err != nil {
		return err
	}
	if err := s.backfillCommentParents(ctx, plan); err != nil {
		return err
	}
	if err := s.rebuildSubscribers(ctx, plan.issues); err != nil {
		return err
	}
	if _, err := s.env.Queries.TransferBumpIssueCounter(ctx, s.env.TargetID); err != nil {
		return fmt.Errorf("bump issue counter: %w", err)
	}
	ws, err := s.env.Queries.GetWorkspace(ctx, s.env.TargetID)
	if err != nil {
		return fmt.Errorf("read issue counter: %w", err)
	}
	s.report.IssueCounter = ws.IssueCounter
	return nil
}

// backfillIssueParents links each issue to its parent. A parent that is not in
// the target workspace is left NULL: hanging the child on a grandparent would
// invent a reporting line that never existed (contract §4.1).
func (s *transferIssueState) backfillIssueParents(ctx context.Context, plan *transferIssuePlan) error {
	for _, issue := range plan.issues {
		if issue.parentSource == "" {
			continue
		}
		parentID := pgUUID(TransferIssueID(s.wsID, issue.parentSource))
		parent, err := s.env.Queries.GetIssue(ctx, parentID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("resolve parent issue: %w", err)
			}
			s.report.ParentUnmapped = append(s.report.ParentUnmapped, UnmappedRef{
				Entity: "issue", SourceID: issue.sourceID, Field: "parent_issue_id",
				RefType: "issue", RefID: issue.parentSource,
				Reason: "parent_unmapped", Resolution: "nulled",
			})
			continue
		}
		if uuidString(parent.WorkspaceID) != s.wsID {
			s.report.ParentUnmapped = append(s.report.ParentUnmapped, UnmappedRef{
				Entity: "issue", SourceID: issue.sourceID, Field: "parent_issue_id",
				RefType: "issue", RefID: issue.parentSource,
				Reason: "parent_unmapped", Resolution: "nulled",
			})
			continue
		}
		n, err := s.env.Queries.TransferBackfillIssueParent(ctx, db.TransferBackfillIssueParentParams{
			ParentIssueID: parentID,
			ID:            issue.targetID,
			WorkspaceID:   s.env.TargetID,
		})
		if err != nil {
			return fmt.Errorf("backfill issue parent: %w", err)
		}
		s.report.ParentsBackfilled += int(n)
	}
	return nil
}

// backfillCommentParents hangs every reply on the nearest ancestor that is
// present in the bundle. A tombstoned or page-truncated parent would otherwise
// flatten the whole thread (contract §4.1 / §11.7).
func (s *transferIssueState) backfillCommentParents(ctx context.Context, plan *transferIssuePlan) error {
	sourceParent := map[string]string{}
	for _, comment := range plan.comments {
		if comment.parentSource != "" {
			sourceParent[comment.sourceID] = comment.parentSource
		}
	}
	for _, comment := range plan.comments {
		if comment.parentSource == "" {
			continue
		}
		ancestor, hops, found, err := s.nearestCommentAncestor(ctx, sourceParent, comment.parentSource)
		if err != nil {
			return err
		}
		if !found {
			s.report.ParentUnmapped = append(s.report.ParentUnmapped, UnmappedRef{
				Entity: "comment", SourceID: comment.sourceID, Field: "parent_id",
				RefType: "comment", RefID: comment.parentSource,
				Reason: "parent_unmapped", Resolution: "nulled",
			})
			continue
		}
		n, err := s.env.Queries.TransferBackfillCommentParent(ctx, db.TransferBackfillCommentParentParams{
			ParentID:    ancestor,
			ID:          comment.targetID,
			WorkspaceID: s.env.TargetID,
		})
		if err != nil {
			return fmt.Errorf("backfill comment parent: %w", err)
		}
		s.report.ParentsBackfilled += int(n)
		if hops > 0 {
			s.report.ParentReparentedToAncestor = append(s.report.ParentReparentedToAncestor, UnmappedRef{
				Entity: "comment", SourceID: comment.sourceID, Field: "parent_id",
				RefType: "comment", RefID: comment.parentSource,
				Reason: "parent_reparented_to_ancestor", Resolution: fmt.Sprintf("ancestor_%d_hops", hops),
			})
		}
	}
	return nil
}

// nearestCommentAncestor walks the source parent chain until it finds a
// comment that actually exists on the target, and reports how many links were
// skipped.
func (s *transferIssueState) nearestCommentAncestor(ctx context.Context, sourceParent map[string]string, start string) (pgtype.UUID, int, bool, error) {
	current := start
	for hops := 0; hops <= len(sourceParent); hops++ {
		if current == "" {
			return pgtype.UUID{}, 0, false, nil
		}
		targetID := pgUUID(TransferCommentID(s.wsID, current))
		if _, err := s.env.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
			ID:          targetID,
			WorkspaceID: s.env.TargetID,
		}); err == nil {
			return targetID, hops, true, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, 0, false, fmt.Errorf("resolve comment ancestor: %w", err)
		}
		next, ok := sourceParent[current]
		if !ok {
			return pgtype.UUID{}, 0, false, nil
		}
		current = next
	}
	// A cycle in the source chain cannot come from this product; stop walking
	// instead of looping forever on a corrupt bundle.
	return pgtype.UUID{}, 0, false, nil
}

// rebuildSubscribers recreates the two subscriptions the event listeners would
// have written on a normal create. Without them nothing that happens on an
// imported issue ever reaches anyone's inbox (contract §6.2).
func (s *transferIssueState) rebuildSubscribers(ctx context.Context, issues []transferPlannedIssue) error {
	for _, issue := range issues {
		if issue.linkOnly {
			continue
		}
		rows := []db.TransferInsertIssueSubscriberParams{{
			IssueID:   issue.targetID,
			UserType:  issue.creatorType,
			UserID:    issue.creatorID,
			Reason:    "creator",
			CreatedAt: issue.createdAt,
		}}
		// A squad is a routing object with no inbox, and a subscription to
		// yourself is one row, not two.
		if (issue.assigneeType == "member" || issue.assigneeType == "agent") &&
			!(issue.assigneeType == issue.creatorType && uuidString(issue.assigneeID) == uuidString(issue.creatorID)) {
			rows = append(rows, db.TransferInsertIssueSubscriberParams{
				IssueID:   issue.targetID,
				UserType:  issue.assigneeType,
				UserID:    issue.assigneeID,
				Reason:    "assignee",
				CreatedAt: issue.createdAt,
			})
		}
		for _, row := range rows {
			n, err := s.env.Queries.TransferInsertIssueSubscriber(ctx, row)
			if err != nil {
				return fmt.Errorf("insert issue subscriber: %w", err)
			}
			if n == 0 {
				s.report.SubscribersSkipped++
			} else {
				s.report.SubscribersCreated++
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Validation helpers
// ---------------------------------------------------------------------------

// rejectTransferIssueSecrets refuses a shard whose metadata / properties bags
// still carry a secret-named key. The export side scrubs these before the
// bundle is written, so a hit means the bundle was not produced by a current
// exporter (contract §10.3).
func rejectTransferIssueSecrets(rows []TransferIssueRow) error {
	for _, row := range rows {
		for _, raw := range []json.RawMessage{row.Metadata, row.Properties} {
			if len(raw) == 0 {
				continue
			}
			var tree any
			if err := json.Unmarshal(raw, &tree); err != nil {
				return transferIssuesInvalid("issue " + row.SourceID + " metadata/properties must be JSON")
			}
			if err := findTransferSecretKey(tree, ""); err != nil {
				return &ImportError{
					Status: 400,
					Code:   "transfer_bundle_contains_secret",
					Msg:    "issue " + row.SourceID + " contains a secret field: " + err.Error(),
				}
			}
		}
	}
	return nil
}

func findTransferSecretKey(v any, path string) error {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			full := joinJSONPath(path, k)
			if child != nil && secretKeyName(k) {
				return fmt.Errorf("%s", full)
			}
			if err := findTransferSecretKey(child, full); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range t {
			if err := findTransferSecretKey(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func transferIssuesInvalid(msg string) error {
	return &ImportError{Status: 400, Code: "transfer_bundle_invalid", Msg: msg}
}

// transferTime parses a bundle timestamp. Unlike parseTransferTime it never
// falls back to now(): a silent fallback is exactly the timestamp corruption
// the dedicated write path exists to prevent.
func transferTime(raw string) (pgtype.Timestamptz, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return pgtype.Timestamptz{}, errors.New("missing timestamp")
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return pgtype.Timestamptz{Time: t.UTC(), Valid: true}, nil
		}
	}
	return pgtype.Timestamptz{}, fmt.Errorf("invalid timestamp %q", raw)
}

func transferDate(raw *string) pgtype.Date {
	value := strings.TrimSpace(derefString(raw))
	if value == "" {
		return pgtype.Date{}
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, value); err == nil {
			return pgtype.Date{Time: t, Valid: true}
		}
	}
	return pgtype.Date{}
}

func transferInt4(v *int32) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *v, Valid: true}
}

func transferText(v *string) pgtype.Text {
	if v == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *v, Valid: true}
}

func optionalString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// transferEnum keeps a free-text column inside its CHECK by falling back to a
// value the table definitely accepts.
func transferEnum(value string, allowed []string, fallback string) string {
	if value == "" {
		return fallback
	}
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return fallback
}

// transferJSONObject normalises an optional JSONB value bag. An empty value
// means "leave the column at its default".
func transferJSONObject(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, errors.New("must be a JSON object")
	}
	return raw, nil
}

func transferUUIDOrNil(raw string) pgtype.UUID {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return pgtype.UUID{Bytes: uuid.Nil, Valid: true}
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}

func stringFromJSON(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func ImportTransferAttachment(ctx context.Context, env TransferImportEnv, meta TransferAttachmentMeta, body []byte) (*TransferAttachmentReport, error) {
	wsID := uuidString(env.TargetID)
	targetID := TransferAttachmentID(wsID, meta.SourceID)
	report := &TransferAttachmentReport{Applied: true, TargetID: targetID.String()}

	existing, err := env.Queries.GetAttachment(ctx, db.GetAttachmentParams{
		ID:          pgUUID(targetID),
		WorkspaceID: env.TargetID,
	})
	if err == nil && uuidString(existing.ID) != "" {
		report.Skipped = true
		report.Created = false
		report.BodyStored = existing.Url != "" && meta.BodyOmittedReason == nil
		return report, nil
	}

	url := ""
	bodyStored := false
	if len(body) > 0 && meta.BodyOmittedReason == nil {
		if env.Storage == nil {
			return nil, &ImportError{Status: 503, Code: "transfer_storage_unavailable", Msg: "file upload not configured"}
		}
		key := "workspaces/" + wsID + "/" + targetID.String() + pathExt(meta.Filename)
		u, err := env.Storage.Upload(ctx, key, body, meta.ContentType, meta.Filename)
		if err != nil {
			return nil, fmt.Errorf("store attachment: %w", err)
		}
		url = u
		bodyStored = true
	}

	var sess pgtype.UUID
	if meta.ChatSessionID != nil && *meta.ChatSessionID != "" {
		sess = pgUUID(TransferChatSessionID(wsID, *meta.ChatSessionID))
	}
	var msg pgtype.UUID
	if meta.ChatMessageID != nil && *meta.ChatMessageID != "" {
		msg = pgUUID(TransferChatMessageID(wsID, *meta.ChatMessageID))
	}
	// V3 mounts the same row on an issue or a comment instead of a chat
	// session; the ids are the deterministic ones the issue shard wrote.
	var issueID pgtype.UUID
	if meta.IssueID != nil && *meta.IssueID != "" {
		issueID = pgUUID(TransferIssueID(wsID, *meta.IssueID))
	}
	var commentID pgtype.UUID
	if meta.CommentID != nil && *meta.CommentID != "" {
		commentID = pgUUID(TransferCommentID(wsID, *meta.CommentID))
	}
	n, err := env.Queries.TransferInsertAttachment(ctx, db.TransferInsertAttachmentParams{
		ID:            pgUUID(targetID),
		WorkspaceID:   env.TargetID,
		ChatSessionID: sess,
		ChatMessageID: msg,
		IssueID:       issueID,
		CommentID:     commentID,
		UploaderType:  "member",
		UploaderID:    env.ImporterID,
		Filename:      meta.Filename,
		Url:           url,
		ContentType:   meta.ContentType,
		SizeBytes:     meta.SizeBytes,
		CreatedAt:     parseTransferTime(meta.CreatedAt),
	})
	if err != nil {
		return nil, fmt.Errorf("insert attachment: %w", err)
	}
	if n == 0 {
		report.Skipped = true
		return report, nil
	}
	report.Created = true
	report.BodyStored = bodyStored
	if meta.BodyOmittedReason != nil {
		report.Reason = *meta.BodyOmittedReason
	}
	return report, nil
}

func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

func parseTransferTime(s string) pgtype.Timestamptz {
	s = strings.TrimSpace(s)
	if s == "" {
		return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return pgtype.Timestamptz{Time: t, Valid: true}
		}
	}
	return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
}

func pathExt(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return ""
	}
	return name[i:]
}

// transferPinChat appends the imported chat to the importer's sidebar pins.
// Idempotent: a pin already present from a prior shard is left alone.
func transferPinChat(ctx context.Context, env TransferImportEnv, sessionID uuid.UUID) error {
	maxPos, err := env.Queries.GetMaxPinnedItemPosition(ctx, db.GetMaxPinnedItemPositionParams{
		WorkspaceID: env.TargetID,
		UserID:      env.ImporterID,
	})
	if err != nil {
		return err
	}
	_, err = env.Queries.CreatePinnedItem(ctx, db.CreatePinnedItemParams{
		WorkspaceID: env.TargetID,
		UserID:      env.ImporterID,
		ItemType:    "chat",
		ItemID:      pgUUID(sessionID),
		Position:    maxPos + 1,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil
		}
		return err
	}
	return nil
}
