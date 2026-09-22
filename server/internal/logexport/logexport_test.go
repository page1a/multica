package logexport

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func ptrTime(t time.Time) *time.Time { return &t }

func ptrInt(v int) *int { return &v }

func baseInput() Input {
	start := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 19, 10, 0, 12, 0, time.UTC)
	return Input{
		GeneratedAt: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC),
		Scope:       Scope{Kind: ScopeRun},
		Target: Target{
			TaskID:          "01a0b577-06b6-786e-8df6-3ebf70edf6d2",
			IssueID:         "issue-1",
			IssueIdentifier: "DENE-599",
			IssueTitle:      "任务日志一键导出并上报",
			AgentID:         "agent-1",
			AgentName:       "孙悟饭",
		},
		Runs: []Run{{
			TaskID:        "01a0b577-06b6-786e-8df6-3ebf70edf6d2",
			AgentID:       "agent-1",
			AgentName:     "孙悟饭",
			Status:        "failed",
			StartedAt:     ptrTime(start),
			CompletedAt:   ptrTime(end),
			ExitCode:      ptrInt(1),
			FailureReason: "agent_error.tool_timeout",
		}},
		Messages: []Message{{
			TaskID:  "01a0b577-06b6-786e-8df6-3ebf70edf6d2",
			Seq:     1,
			At:      start,
			Type:    "text",
			Content: "worker started",
		}, {
			TaskID: "01a0b577-06b6-786e-8df6-3ebf70edf6d2",
			Seq:    2,
			At:     start.Add(5 * time.Second),
			Type:   "tool_result",
			Tool:   "http",
			Output: "GET /api/sync 返回 504 timeout",
		}},
	}
}

func TestParseScope(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		hours   int
		want    Scope
		wantErr bool
	}{
		{name: "empty defaults to run", raw: "", hours: 0, want: Scope{Kind: ScopeRun}},
		{name: "run", raw: "run", want: Scope{Kind: ScopeRun}},
		{name: "task", raw: "task", want: Scope{Kind: ScopeTask}},
		{name: "hours", raw: "hours", hours: 6, want: Scope{Kind: ScopeHours, Hours: 6}},
		{name: "hours without hours", raw: "hours", hours: 0, wantErr: true},
		{name: "hours above cap", raw: "hours", hours: MaxHours + 1, wantErr: true},
		{name: "run with hours", raw: "run", hours: 6, wantErr: true},
		{name: "unknown", raw: "everything", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseScope(tc.raw, tc.hours)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseScope(%q, %d) = %+v, want error", tc.raw, tc.hours, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseScope(%q, %d) error: %v", tc.raw, tc.hours, err)
			}
			if got != tc.want {
				t.Fatalf("ParseScope(%q, %d) = %+v, want %+v", tc.raw, tc.hours, got, tc.want)
			}
		})
	}
}

func TestBuildRunScopeKeepsOnlyAtomsAndSetsMetadata(t *testing.T) {
	b, err := Build(baseInput())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if b.Format != FormatName || b.Version != FormatVersion {
		t.Fatalf("format = %s/%d, want %s/%d", b.Format, b.Version, FormatName, FormatVersion)
	}
	if b.Task.IssueIdentifier != "DENE-599" || b.Task.AgentName != "孙悟饭" {
		t.Fatalf("task metadata = %+v", b.Task)
	}
	if b.Task.Status != "failed" || b.Task.ExitCode == nil || *b.Task.ExitCode != 1 {
		t.Fatalf("status/exit = %q/%v, want failed/1", b.Task.Status, b.Task.ExitCode)
	}
	if b.RunCount != 1 || b.EntryCount != 2 {
		t.Fatalf("counts = %d runs / %d entries, want 1/2", b.RunCount, b.EntryCount)
	}
	if b.Task.Window.From != "2026-09-19T10:00:00Z" || b.Task.Window.To != "2026-09-19T10:00:12Z" {
		t.Fatalf("window = %+v", b.Task.Window)
	}
}

func TestBuildHoursScopeDropsMessagesOutsideWindow(t *testing.T) {
	in := baseInput()
	in.Scope = Scope{Kind: ScopeHours, Hours: 1}
	// Generate at 10:30 so the window is 09:30–10:30: both base entries
	// (10:00:00 and 10:00:05) survive and the 09:00 entry does not.
	in.GeneratedAt = time.Date(2026, 9, 19, 10, 30, 0, 0, time.UTC)
	old := baseInput().Messages[0]
	old.At = time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC)
	old.Seq = 0
	in.Messages = append([]Message{old}, in.Messages...)

	b, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if b.EntryCount != 2 {
		t.Fatalf("entry count = %d, want 2 (the 09:00 entry is outside now-1h)", b.EntryCount)
	}
	for _, e := range b.Entries {
		if e.Seq == 0 {
			t.Fatalf("entry outside the hours window survived: %+v", e)
		}
	}
	if b.Task.Window.From != "2026-09-19T09:30:00Z" {
		t.Fatalf("hours window from = %q, want 2026-09-19T09:30:00Z", b.Task.Window.From)
	}
}

func TestBuildTaskScopeKeepsEveryRunInOrder(t *testing.T) {
	second := time.Date(2026, 9, 19, 11, 0, 0, 0, time.UTC)
	in := baseInput()
	in.Scope = Scope{Kind: ScopeTask}
	in.Target.TaskID = "task-second"
	in.Runs = []Run{{
		TaskID:    "task-first",
		Status:    "failed",
		StartedAt: ptrTime(time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)),
		ExitCode:  ptrInt(1),
	}, {
		TaskID:    "task-second",
		Status:    "completed",
		StartedAt: ptrTime(second),
		ExitCode:  ptrInt(0),
	}}
	in.Messages = []Message{
		{TaskID: "task-second", Seq: 1, At: second, Type: "text", Content: "second"},
		{TaskID: "task-first", Seq: 1, At: time.Date(2026, 9, 19, 10, 0, 1, 0, time.UTC), Type: "text", Content: "first"},
	}

	b, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if b.RunCount != 2 || b.EntryCount != 2 {
		t.Fatalf("counts = %d/%d, want 2/2", b.RunCount, b.EntryCount)
	}
	// Runs are start-ordered, so the older run's entry must come first even
	// though it arrived second in Messages.
	if b.Entries[0].TaskID != "task-first" || b.Entries[1].TaskID != "task-second" {
		t.Fatalf("entry order = %q,%q", b.Entries[0].TaskID, b.Entries[1].TaskID)
	}
	// The requested run still labels the bundle.
	if b.Task.Status != "completed" || *b.Task.ExitCode != 0 {
		t.Fatalf("task view = %+v, want the requested run's status", b.Task)
	}
}

func TestBuildTruncatesAndFlagsIt(t *testing.T) {
	in := baseInput()
	in.MaxEntries = 1
	b, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !b.Truncated || b.EntryCount != 1 {
		t.Fatalf("truncated=%v entries=%d, want true/1", b.Truncated, b.EntryCount)
	}
	// The cap keeps the newest entries, which is where a failure usually is.
	if b.Entries[0].Seq != 2 {
		t.Fatalf("kept seq = %d, want 2", b.Entries[0].Seq)
	}
	if !strings.Contains(b.SummaryMarkdown, "仅保留最近的部分") {
		t.Fatalf("summary does not disclose truncation:\n%s", b.SummaryMarkdown)
	}
}

func TestSelectRuns(t *testing.T) {
	target := Run{TaskID: "target"}
	runs := []Run{
		{TaskID: "later", StartedAt: ptrTime(time.Date(2026, 9, 19, 11, 0, 0, 0, time.UTC))},
		{TaskID: "earlier", StartedAt: ptrTime(time.Date(2026, 9, 19, 9, 0, 0, 0, time.UTC))},
	}
	got := SelectRuns(Scope{Kind: ScopeRun}, target, runs)
	if len(got) != 1 || got[0].TaskID != "target" {
		t.Fatalf("run scope = %+v, want the target alone", got)
	}
	got = SelectRuns(Scope{Kind: ScopeTask}, target, runs)
	if len(got) != 2 || got[0].TaskID != "earlier" || got[1].TaskID != "later" {
		t.Fatalf("task scope = %+v, want start-ordered history", got)
	}
}

func TestBuildRedactsSecretsAndEnvValues(t *testing.T) {
	const (
		githubToken = "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn"
		dbPassword  = "hunter2-correct-horse"
		boring      = "us-east-1"
	)
	in := baseInput()
	in.Env = map[string]string{
		"GITHUB_TOKEN":      githubToken,
		"DATABASE_PASSWORD": dbPassword,
		"AWS_REGION":        boring,
		"SHORT_TOKEN":       "ab",
	}
	in.Messages = append(in.Messages, Message{
		TaskID:  in.Target.TaskID,
		Seq:     3,
		At:      time.Date(2026, 9, 19, 10, 0, 6, 0, time.UTC),
		Type:    "tool_use",
		Tool:    "bash",
		Content: "running with GITHUB_TOKEN=" + githubToken + " and password " + dbPassword + " in region " + boring,
		Input: map[string]any{
			"cmd": "connect",
			"env": map[string]any{
				"DATABASE_PASSWORD": dbPassword,
				"nested":            []any{githubToken, "keep-me"},
			},
		},
	})

	b, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	body, err := Marshal(b)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	rendered := string(body)
	for _, secret := range []string{githubToken, dbPassword, "ghp_", "hunter2"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("artifact leaked %q:\n%s", secret, rendered)
		}
	}
	// A non-secret variable and a too-short value must survive: over-masking
	// everything would make the export useless.
	if !strings.Contains(rendered, boring) {
		t.Fatalf("artifact masked a non-secret env value %q", boring)
	}
	if !strings.Contains(rendered, "keep-me") {
		t.Fatalf("artifact masked an ordinary nested string")
	}
	if !strings.Contains(rendered, envValuePlaceholder) {
		t.Fatalf("expected literal env masking to leave a placeholder")
	}
}

func TestNewMaskerDenylist(t *testing.T) {
	m := NewMasker(map[string]string{
		"OPENAI_API_KEY": "sk-live-abcdefghijklmnopqrstuvwxyz",
		"AWS_REGION":     "eu-west-1",
		"DEBUG":          "true",
		"SHORT_TOKEN":    "ab",
	})
	lits := m.SecretLiterals()
	if len(lits) != 1 || lits[0] != "sk-live-abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("literals = %v, want only the deny-listed long value", lits)
	}
	if got := m.Text("region eu-west-1 debug true"); got != "region eu-west-1 debug true" {
		t.Fatalf("non-secret text changed: %q", got)
	}
	if got := m.Text("key sk-live-abcdefghijklmnopqrstuvwxyz!"); strings.Contains(got, "sk-live") {
		t.Fatalf("literal not masked: %q", got)
	}
}

func TestMaskerLongestLiteralWins(t *testing.T) {
	m := NewMasker(map[string]string{
		"API_TOKEN":    "abcdef",
		"API_SECRET":   "abcdef-extra",
		"API_PASSWORD": "abcdef-extra",
	})
	if got := m.Text("value abcdef-extra"); strings.Contains(got, "extra") {
		t.Fatalf("a shorter literal left a tail behind: %q", got)
	}
}

func TestMaskerValueDepthLimit(t *testing.T) {
	m := NewMasker(nil)
	var nested any = "leaf"
	for i := 0; i < maxRedactDepth+2; i++ {
		nested = map[string]any{"child": nested}
	}
	got := m.Value(nested)
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "leaf") {
		t.Fatalf("depth-limited value leaked: %s", body)
	}
	if !strings.Contains(string(body), truncatedPlaceholder) {
		t.Fatalf("depth limit placeholder missing: %s", body)
	}
}

func TestSummarize(t *testing.T) {
	b, err := Build(baseInput())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	s := b.SummaryMarkdown
	for _, want := range []string{"DENE-599", "孙悟饭", "退出码: 1", "本次运行", "GET /api/sync 返回 504 timeout", "脱敏"} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary missing %q:\n%s", want, s)
		}
	}
}

func TestSummarizeReportsNoExitCodeAsUnknown(t *testing.T) {
	in := baseInput()
	in.Runs[0].Status = "cancelled"
	in.Runs[0].ExitCode = nil
	b, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(b.SummaryMarkdown, "退出码: 未知") || !strings.Contains(b.SummaryMarkdown, "被取消") {
		t.Fatalf("summary = %s", b.SummaryMarkdown)
	}
}

// TestBuildMarksIncompleteRedaction is the artifact side of F3. When the
// exported runs' environment could not be read, the value deny-list never ran,
// and the bundle must say so rather than shipping the reassuring
// "已自动脱敏" line; the scenario is a deleted agent and a password with no
// recognizable token shape.
func TestBuildMarksIncompleteRedaction(t *testing.T) {
	complete, err := Build(baseInput())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !complete.Redaction.Complete || !complete.Redaction.EnvDenyList || !complete.Redaction.PatternRules {
		t.Fatalf("complete bundle redaction = %+v", complete.Redaction)
	}
	if !strings.Contains(complete.SummaryMarkdown, "已自动脱敏") {
		t.Fatalf("complete summary does not reassure the reader:\n%s", complete.SummaryMarkdown)
	}

	in := baseInput()
	in.EnvLookupGap = "agent 记录不存在"
	in.Messages = append(in.Messages, Message{
		TaskID:  in.Target.TaskID,
		Seq:     3,
		At:      in.Messages[0].At.Add(6 * time.Second),
		Type:    "text",
		Content: "DATABASE_PASSWORD=hunter2-correct-horse",
	})
	degraded, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if degraded.Redaction.Complete || degraded.Redaction.EnvDenyList {
		t.Fatalf("degraded bundle still claims a complete redaction: %+v", degraded.Redaction)
	}
	if degraded.Redaction.Note != "agent 记录不存在" {
		t.Fatalf("degraded note = %q", degraded.Redaction.Note)
	}
	if strings.Contains(degraded.SummaryMarkdown, "已自动脱敏") {
		t.Fatalf("degraded summary still reassures the reader:\n%s", degraded.SummaryMarkdown)
	}
	if !strings.Contains(degraded.SummaryMarkdown, "脱敏不完整") {
		t.Fatalf("degraded summary does not warn the reader:\n%s", degraded.SummaryMarkdown)
	}

	body, err := Marshal(degraded)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(body), `"complete": false`) {
		t.Fatalf("artifact does not carry the redaction marker:\n%s", body)
	}
}

func TestMarshalIsStableAndParseable(t *testing.T) {
	in := baseInput()
	first, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	second, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	a, _ := Marshal(first)
	b, _ := Marshal(second)
	if string(a) != string(b) {
		t.Fatalf("marshal is not deterministic:\n%s\n---\n%s", a, b)
	}
	var round Bundle
	if err := json.Unmarshal(a, &round); err != nil {
		t.Fatalf("artifact is not valid JSON: %v", err)
	}
	if round.Format != FormatName || round.EntryCount != first.EntryCount {
		t.Fatalf("round trip = %+v", round)
	}
}

func TestFileNameSanitizesUnsafeRunes(t *testing.T) {
	in := baseInput()
	in.Target.IssueIdentifier = "DENE/599: weird"
	b, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	name := FileName(b)
	if strings.ContainsAny(name, "/: ") {
		t.Fatalf("file name kept unsafe characters: %q", name)
	}
	if !strings.HasPrefix(name, "log-export-") || !strings.HasSuffix(name, ".json") {
		t.Fatalf("file name = %q", name)
	}
}
