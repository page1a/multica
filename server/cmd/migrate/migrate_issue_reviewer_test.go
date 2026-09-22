package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 503 moves 验收席 off a workspace `select` property and onto the issue as a
// reference pair. The migration's whole value is in the backfill: the option
// list holds seat NAMES, and this is the last moment those names still match
// the roster, so a row it fails to translate here is a reviewer decision the
// workspace loses. This test walks each option meaning through the migration
// and checks the row that comes out the other side. (DENE-633)
func TestIssueReviewerMigrationBackfillsEveryOptionMeaning(t *testing.T) {
	adminPool := openTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	schema := fmt.Sprintf("migrate_issue_reviewer_%d_%d", time.Now().UnixNano(), rand.Uint32())
	schemaIdent := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+schemaIdent); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+schemaIdent+" CASCADE"); err != nil {
			t.Logf("drop schema %s: %v", schema, err)
		}
	})

	pool := openTestPoolWithSearchPath(t, schema)
	createIssueReviewerFixture(t, ctx, pool)

	options := runOptions{
		Direction:             "up",
		Files:                 realMigrationFiles(t, []string{"507_issue_reviewer"}, "up"),
		SchemaMigrationsTable: schema + ".schema_migrations",
		AdvisoryLockKey:       int64(rand.Uint64()&0x7fffffffffffffff) | 1,
		Hooks:                 hooksForDirection("up"),
	}
	if err := runMigrations(ctx, pool, options); err != nil {
		t.Fatalf("apply 507_issue_reviewer up: %v", err)
	}

	cases := []struct {
		title        string
		wantType     string
		wantID       string
		wantIDReason string
	}{
		{"no-review issue", "none", "", "'不需要验收' is an answer, and it carries no id"},
		{"agent issue", "agent", agentUUID, "the option name still matches a roster seat"},
		{"human issue", "member", memberUUID, "'交给人' meant the member who created the ticket"},
		{"agent-created human issue", "member", ownerUUID, "an agent-created ticket falls back to the workspace owner"},
		{"unset issue", "", "", "an issue that never chose stays an empty slot routing may fill"},
		{"stale option issue", "", "", "an option naming a seat that no longer exists resolves to nobody"},
		{"other workspace issue", "", "", "the property belongs to one workspace and must not reach another"},
	}
	for _, tc := range cases {
		gotType, gotID := readReviewerPair(t, ctx, pool, tc.title)
		if gotType != tc.wantType || gotID != tc.wantID {
			t.Errorf("%s: pair = (%q, %q), want (%q, %q) — %s",
				tc.title, gotType, gotID, tc.wantType, tc.wantID, tc.wantIDReason)
		}
	}

	// The property is retired rather than deleted, so a workspace never shows
	// two reviewer fields that can disagree.
	var archived bool
	if err := pool.QueryRow(ctx,
		`SELECT archived_at IS NOT NULL FROM issue_property WHERE name = '验收席'`,
	).Scan(&archived); err != nil {
		t.Fatalf("read property archived_at: %v", err)
	}
	if !archived {
		t.Error("the 验收席 property is still active after the migration, so the picker still offers it")
	}

	// The check constraint is what keeps a fourth reviewer_type from appearing
	// later without a matching branch in the routing table.
	if _, err := pool.Exec(ctx,
		`UPDATE issue SET reviewer_type = 'squad' WHERE title = 'unset issue'`,
	); err == nil {
		t.Error("reviewer_type accepted 'squad'; the check constraint is missing")
	}

	down := runOptions{
		Direction:             "down",
		Files:                 realMigrationFiles(t, []string{"507_issue_reviewer"}, "down"),
		SchemaMigrationsTable: schema + ".schema_migrations",
		AdvisoryLockKey:       options.AdvisoryLockKey,
		Hooks:                 hooksForDirection("down"),
	}
	if err := runMigrations(ctx, pool, down); err != nil {
		t.Fatalf("apply 507_issue_reviewer down: %v", err)
	}

	var columns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = 'issue'
		  AND column_name IN ('reviewer_type', 'reviewer_id')
	`, schema).Scan(&columns); err != nil {
		t.Fatalf("read issue columns after down: %v", err)
	}
	if columns != 0 {
		t.Errorf("%d reviewer columns survive the down migration, want 0", columns)
	}
	if err := pool.QueryRow(ctx,
		`SELECT archived_at IS NOT NULL FROM issue_property WHERE name = '验收席'`,
	).Scan(&archived); err != nil {
		t.Fatalf("read property archived_at after down: %v", err)
	}
	if archived {
		t.Error("the down migration left the property archived, so a rollback has no reviewer field at all")
	}
}

const (
	workspaceUUID      = "00000000-0000-0000-0000-000000000001"
	otherWorkspaceUUID = "00000000-0000-0000-0000-000000000002"
	propertyUUID       = "00000000-0000-0000-0000-000000000011"
	agentUUID          = "00000000-0000-0000-0000-000000000021"
	memberUUID         = "00000000-0000-0000-0000-000000000031"
	ownerUUID          = "00000000-0000-0000-0000-000000000032"
)

func readReviewerPair(t *testing.T, ctx context.Context, pool *pgxpool.Pool, title string) (string, string) {
	t.Helper()
	var reviewerType, reviewerID *string
	if err := pool.QueryRow(ctx,
		`SELECT reviewer_type, reviewer_id::text FROM issue WHERE title = $1`, title,
	).Scan(&reviewerType, &reviewerID); err != nil {
		t.Fatalf("read reviewer pair for %q: %v", title, err)
	}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return deref(reviewerType), deref(reviewerID)
}

// createIssueReviewerFixture builds only the columns 503 reads: the schema in
// production is much wider, and naming the rest here would make the fixture
// stale the first time an unrelated column changes.
func createIssueReviewerFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	statements := []string{
		`CREATE TABLE schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE issue (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			workspace_id UUID NOT NULL,
			title TEXT NOT NULL,
			creator_type TEXT NOT NULL,
			creator_id UUID,
			properties JSONB NOT NULL DEFAULT '{}'::jsonb
		)`,
		`CREATE TABLE issue_property (
			id UUID PRIMARY KEY,
			workspace_id UUID NOT NULL,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			config JSONB NOT NULL DEFAULT '{}'::jsonb,
			archived_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE agent (
			id UUID PRIMARY KEY,
			workspace_id UUID NOT NULL,
			name TEXT NOT NULL
		)`,
		`CREATE TABLE member (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			workspace_id UUID NOT NULL,
			user_id UUID NOT NULL,
			role TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("apply fixture statement %q: %v", statement, err)
		}
	}

	seed := fmt.Sprintf(`
		INSERT INTO issue_property (id, workspace_id, name, type, config) VALUES (
			'%[2]s', '%[1]s', '验收席', 'select',
			'{"options": [
				{"id": "opt-none", "name": "不需要验收"},
				{"id": "opt-agent", "name": "布尔玛游戏"},
				{"id": "opt-gone", "name": "已离职的席位"},
				{"id": "opt-human", "name": "交给人"}
			]}'::jsonb
		);

		INSERT INTO agent (id, workspace_id, name)
		VALUES ('%[3]s', '%[1]s', '布尔玛游戏');

		INSERT INTO member (workspace_id, user_id, role, created_at) VALUES
			('%[1]s', '%[5]s', 'owner', now() - interval '1 day'),
			('%[1]s', '%[4]s', 'member', now());

		INSERT INTO issue (workspace_id, title, creator_type, creator_id, properties) VALUES
			('%[1]s', 'no-review issue',            'member', '%[4]s', '{"%[2]s": "opt-none"}'::jsonb),
			('%[1]s', 'agent issue',                'member', '%[4]s', '{"%[2]s": "opt-agent"}'::jsonb),
			('%[1]s', 'human issue',                'member', '%[4]s', '{"%[2]s": "opt-human"}'::jsonb),
			('%[1]s', 'agent-created human issue',  'agent',  '%[3]s', '{"%[2]s": "opt-human"}'::jsonb),
			('%[1]s', 'stale option issue',         'member', '%[4]s', '{"%[2]s": "opt-gone"}'::jsonb),
			('%[1]s', 'unset issue',                'member', '%[4]s', '{}'::jsonb),
			('%[6]s', 'other workspace issue',      'member', '%[4]s', '{"%[2]s": "opt-agent"}'::jsonb);
	`, workspaceUUID, propertyUUID, agentUUID, memberUUID, ownerUUID, otherWorkspaceUUID)
	if _, err := pool.Exec(ctx, seed); err != nil {
		t.Fatalf("seed reviewer fixture: %v", err)
	}
}
