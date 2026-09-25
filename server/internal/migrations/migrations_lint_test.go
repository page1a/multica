package migrations

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// knownDuplicatePrefixStems records the numeric prefixes that carry exactly two
// migrations, one from each of two lines that ran in parallel: the upstream
// mirror and this fork added 468–501 independently, and golang-migrate applies
// both sets (it keys on the full filename, not the number). Neither migration
// may be renumbered now — both are applied in existing workspaces, and a rename
// would replay or skip schema changes there — so the lint treats these pairs as
// historical and keeps failing on any NEW collision.
//
// The value is the exact stem set expected at that number: a third migration
// landing on an exempted number, or a pair being retargeted, still fails below.
var knownDuplicatePrefixStems = map[int][]string{
	468: {"468_comment_deleted_at", "468_drop_reference_only_column"},
	469: {"469_drop_reference_only_column", "469_issue_status_lifecycle_categories"},
	470: {"470_agent_runtime_plan_limits", "470_issue_status_icon"},
	471: {"471_comment_deleted_at", "471_user_password_credential"},
	472: {"472_agent_task_queue_chat_session_index", "472_signup_totp_used_step"},
	473: {"473_agent_switchable_models", "473_drop_agent_task_queue_chat_with_session_index"},
	474: {"474_agent_auto_retry", "474_dingtalk_bot_identity_workspace_index"},
	475: {"475_issue_status_key_not_reserved", "475_project_member"},
	476: {"476_project_member_unique", "476_reserve_triage_status_key"},
	477: {"477_issue_effective_status_triage", "477_project_member_project_index"},
	478: {"478_issue_status_category_expand", "478_project_member_member_index"},
	479: {"479_instance_telemetry_state", "479_issue_view_project_visibility"},
	480: {"480_instance_telemetry_state_singleton_index", "480_stage_wakeup_failure"},
	481: {"481_instance_telemetry_state_primary_key", "481_stage_wakeup_failure_unswept_index"},
	482: {"482_agent_task_queue_telemetry_started_index", "482_stage_wakeup_failure_parent_index"},
	483: {"483_issue_draft", "483_issue_triage_state"},
	484: {"484_issue_origin_issue_draft", "484_issue_triage_state_index"},
	485: {"485_issue_origin_issue_draft_validate", "485_maintenance_job"},
	486: {"486_issue_draft_origin_unique", "486_maintenance_job_id_index"},
	487: {"487_agent_parent", "487_maintenance_job_idempotency_index"},
	488: {"488_agent_parent_index", "488_maintenance_job_active_index"},
	489: {"489_issue_draft_policy", "489_issue_triage_state_validate"},
	490: {"490_drop_triage_status_key_reservation", "490_issue_draft_finalize_round"},
	491: {"491_issue_status_category_backfill", "491_transfer_attachment_upload"},
	492: {"492_agent_runtime_inherited", "492_issue_status_category_contract"},
	493: {"493_chat_session_project", "493_issue_status_category_validate"},
	494: {"494_chat_session_project_unique", "494_issue_status_category_read_contract"},
	495: {"495_chat_session_project_project_index", "495_issue_to_label_label_id_index"},
	496: {"496_chat_session_agent_id_index", "496_chat_session_project_backfill"},
	497: {"497_agent_runtime_jev_status", "497_agent_task_queue_delegated_failure_evidence_index"},
	498: {"498_chat_session_runtime_id_index", "498_project_resource_local_directory_identity"},
	499: {"499_agent_task_issue_snapshot", "499_project_resource_local_directory_repo"},
	500: {"500_task_message_call_id", "500_task_usage_run_metadata"},
	501: {"501_agent_routing_tier", "501_runtime_profile_runtime_type"},
}

// firstUniqueMigrationNumber: migrations through 128 contain historical
// duplicate numeric prefixes. From 129 onward, keep the numeric sequence unique
// so release tooling and operators can identify one schema change unambiguously
// by its number. 468–501 are exempted as the documented upstream/fork overlap
// above; that window is closed (see TestMigrationKnownDuplicateWhitelistIsFrozen).
const firstUniqueMigrationNumber = 129

// Allocation rules and the CI/release wiring of this gate are documented in
// docs/kun/migration-numbering.md. Keep that runbook in step with this file.
func TestMigrationNumericPrefixesAreUnique(t *testing.T) {
	files := migrationFilesForLint(t, "*.up.sql")
	stems := make([]string, 0, len(files))
	for _, file := range files {
		stem, _, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		stems = append(stems, stem)
	}

	for _, problem := range migrationPrefixCollisions(stems, knownDuplicatePrefixStems, firstUniqueMigrationNumber) {
		t.Error(problem)
	}
}

// TestMigrationKnownDuplicateWhitelistIsFrozen pins the shape of the historical
// exemption: it covers exactly 468–501, every number carries exactly two stems,
// and every stem still exists on disk. Extending the map to admit a new
// collision, or a fork-side renumbering that silently drops an exempted file,
// fails here rather than being absorbed by the collision check above.
func TestMigrationKnownDuplicateWhitelistIsFrozen(t *testing.T) {
	const firstExempt, lastExempt = 468, 501

	files := migrationFilesForLint(t, "*.up.sql")
	onDisk := make(map[string]bool, len(files))
	for _, file := range files {
		if stem, _, ok := splitMigrationFilename(filepath.Base(file)); ok {
			onDisk[stem] = true
		}
	}

	if got, want := len(knownDuplicatePrefixStems), lastExempt-firstExempt+1; got != want {
		t.Errorf("whitelist has %d numbers, want exactly %d (%d–%d)", got, want, firstExempt, lastExempt)
	}
	for number, stems := range knownDuplicatePrefixStems {
		if number < firstExempt || number > lastExempt {
			t.Errorf("whitelist number %d is outside the frozen %d–%d window", number, firstExempt, lastExempt)
		}
		if len(stems) != 2 {
			t.Errorf("whitelist number %d lists %d stems, want exactly 2", number, len(stems))
		}
		for _, stem := range stems {
			prefix, _, _ := strings.Cut(stem, "_")
			if strconv.Itoa(number) != prefix {
				t.Errorf("whitelist stem %q is filed under %d but carries prefix %q", stem, number, prefix)
			}
			if !onDisk[stem] {
				t.Errorf("whitelist stem %q has no %s.up.sql on disk", stem, stem)
			}
		}
	}
}

// TestMigrationPrefixCollisionsDetectsNewCollisions is the gate's own guard:
// it feeds synthetic stem sets to the pure check so CI keeps proving that a
// fresh collision, a third file on an exempted number, and a retargeted pair
// all fail, while the exact whitelist and the pre-129 history pass.
func TestMigrationPrefixCollisionsDetectsNewCollisions(t *testing.T) {
	exempt := map[int][]string{
		500: {"500_task_message_call_id", "500_task_usage_run_metadata"},
	}
	cases := []struct {
		name  string
		stems []string
		want  []string
	}{
		{
			name:  "unique sequence passes",
			stems: []string{"523_a", "524_b", "525_c"},
		},
		{
			name:  "exact exempted pair passes",
			stems: []string{"500_task_message_call_id", "500_task_usage_run_metadata", "501_x"},
		},
		{
			name:  "history before 129 is ignored",
			stems: []string{"128_a", "128_b", "129_c"},
		},
		{
			name:  "new collision fails",
			stems: []string{"524_agent_plan_limits", "524_something_else"},
			want:  []string{"migrations 524_agent_plan_limits and 524_something_else share numeric prefix 524"},
		},
		{
			name:  "third file on an exempted number fails",
			stems: []string{"500_task_message_call_id", "500_task_usage_run_metadata", "500_third"},
			want: []string{
				"migrations 500_task_message_call_id and 500_task_usage_run_metadata share numeric prefix 500",
				"migrations 500_task_message_call_id and 500_third share numeric prefix 500",
			},
		},
		{
			name:  "retargeted exempted pair fails",
			stems: []string{"500_task_message_call_id", "500_renamed"},
			want:  []string{"migrations 500_renamed and 500_task_message_call_id share numeric prefix 500"},
		},
		{
			name:  "non-numeric prefixes are skipped",
			stems: []string{"schema_a", "schema_b", "525_c"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := migrationPrefixCollisions(tc.stems, exempt, firstUniqueMigrationNumber)
			if !equalStrings(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// migrationPrefixCollisions reports, in deterministic order, every numeric
// prefix at or above firstUnique that is shared by more than one stem, unless
// the stems at that number are exactly the exempted set.
func migrationPrefixCollisions(stems []string, exempt map[int][]string, firstUnique int) []string {
	stemsByNumber := make(map[int][]string)
	for _, stem := range stems {
		prefix, _, ok := strings.Cut(stem, "_")
		if !ok {
			continue
		}
		number, err := strconv.Atoi(prefix)
		if err != nil || number < firstUnique {
			continue
		}
		stemsByNumber[number] = append(stemsByNumber[number], stem)
	}

	numbers := make([]int, 0, len(stemsByNumber))
	for number := range stemsByNumber {
		numbers = append(numbers, number)
	}
	sort.Ints(numbers)

	var problems []string
	for _, number := range numbers {
		stems := stemsByNumber[number]
		if len(stems) < 2 {
			continue
		}
		if known, ok := exempt[number]; ok && sameStems(stems, known) {
			continue
		}
		sort.Strings(stems)
		for _, stem := range stems[1:] {
			problems = append(problems, fmt.Sprintf("migrations %s and %s share numeric prefix %d", stems[0], stem, number))
		}
	}
	return problems
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func sameStems(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	gotSorted := append([]string(nil), got...)
	wantSorted := append([]string(nil), want...)
	sort.Strings(gotSorted)
	sort.Strings(wantSorted)
	for i := range gotSorted {
		if gotSorted[i] != wantSorted[i] {
			return false
		}
	}
	return true
}

func TestMigrationFilesHaveMatchingDirections(t *testing.T) {
	files := migrationFilesForLint(t, "*.sql")
	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, filepath.Base(file))
	}
	for _, problem := range unpairedMigrationStems(names) {
		t.Error(problem)
	}
}

func TestMigrationUnpairedStemsDetectsMissingDirection(t *testing.T) {
	got := unpairedMigrationStems([]string{
		"524_a.up.sql", "524_a.down.sql",
		"525_b.up.sql",
		"526_c.down.sql",
		"README.md",
	})
	want := []string{
		"migration 525_b must have both .up.sql and .down.sql files",
		"migration 526_c must have both .up.sql and .down.sql files",
	}
	if !equalStrings(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// unpairedMigrationStems reports, in deterministic order, every stem that has
// an up or a down file but not both.
func unpairedMigrationStems(names []string) []string {
	directionsByStem := make(map[string]map[string]bool)
	for _, name := range names {
		stem, direction, ok := splitMigrationFilename(name)
		if !ok {
			continue
		}
		if directionsByStem[stem] == nil {
			directionsByStem[stem] = make(map[string]bool)
		}
		directionsByStem[stem][direction] = true
	}

	stems := make([]string, 0, len(directionsByStem))
	for stem := range directionsByStem {
		stems = append(stems, stem)
	}
	sort.Strings(stems)

	var problems []string
	for _, stem := range stems {
		directions := directionsByStem[stem]
		if !directions["up"] || !directions["down"] {
			problems = append(problems, fmt.Sprintf("migration %s must have both .up.sql and .down.sql files", stem))
		}
	}
	return problems
}

func migrationFilesForLint(t *testing.T, pattern string) []string {
	t.Helper()

	dir := realMigrationsDir(t)
	files, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no migration files matched %s in %s", pattern, dir)
	}
	sort.Strings(files)
	return files
}

func realMigrationsDir(t *testing.T) string {
	t.Helper()

	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration lint test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", "migrations"))
}

func splitMigrationFilename(name string) (stem, direction string, ok bool) {
	for _, candidateDirection := range []string{"up", "down"} {
		suffix := fmt.Sprintf(".%s.sql", candidateDirection)
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix), candidateDirection, true
		}
	}
	return "", "", false
}
