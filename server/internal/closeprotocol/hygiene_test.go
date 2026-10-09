package closeprotocol

import (
	"strings"
	"testing"
)

func TestMemorySectionsSplitsHeadingsAndSeesSupersedeMarks(t *testing.T) {
	content := "# Map\nintro\n## 工作单\n- 规则 A\n```\n## not a heading\n```\n## 旧派单\n- 已被「自动派票」取代\n"
	got := MemorySections(content)
	if len(got) != 3 || got[0].Heading != "Map" || got[1].Heading != "工作单" || got[2].Heading != "旧派单" {
		t.Fatalf("sections = %+v", got)
	}
	if got[1].Superseded || !got[2].Superseded {
		t.Fatalf("superseded flags = %+v", got)
	}
	total := 0
	for _, s := range got {
		total += s.Bytes
	}
	if total != len(content) {
		t.Fatalf("section bytes %d != content %d", total, len(content))
	}
}

func TestCanonicalKnowledgeAuditChecksActions(t *testing.T) {
	ok := KnowledgeAudit{Changes: []KnowledgeChange{
		{Location: "agents", Action: "UPDATE", Entry: "工作单", Summary: "改了"},
		{Location: "agents", Action: "supersede", Entry: "旧派单", Summary: "被自动派票取代"},
		{Location: "context", Action: "new", Summary: "新词条"},
	}}
	got, _, err := CanonicalKnowledgeAudit(ok)
	if err != nil || got.Changes[0].Action != "update" {
		t.Fatalf("audit = %+v err = %v", got, err)
	}
	for name, tc := range map[string]struct {
		change KnowledgeChange
		want   string
	}{
		"unknown action":      {KnowledgeChange{Location: "agents", Action: "delete", Summary: "s"}, "不认识"},
		"update needs entry":  {KnowledgeChange{Location: "agents", Action: "update", Summary: "s"}, "已有条目"},
		"merge needs entry":   {KnowledgeChange{Location: "agents", Action: "merge", Summary: "s"}, "已有条目"},
		"same entry twice":    {KnowledgeChange{Location: "agents", Action: "update", Entry: "工作单", Summary: "s"}, "写了两次"},
		"plain location dupe": {KnowledgeChange{Location: "context", Summary: "s"}, "写了两次"},
	} {
		audit := KnowledgeAudit{Changes: append(append([]KnowledgeChange(nil), ok.Changes...), tc.change)}
		if _, _, err := CanonicalKnowledgeAudit(audit); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestCheckMemoryHygiene(t *testing.T) {
	update := KnowledgeAudit{Changes: []KnowledgeChange{{Location: "agents", Action: ActionUpdate, Entry: "工作单", Summary: "s"}}}
	bare := KnowledgeAudit{Changes: []KnowledgeChange{{Location: "agents", Summary: "s"}}}
	supersede := KnowledgeAudit{Changes: []KnowledgeChange{{Location: "agents", Action: ActionSupersede, Entry: "旧派单", Summary: "被自动派票取代"}}}
	small := func(f MemoryFile) *[]MemoryFile { f.Path = "AGENTS.md"; f.Bytes = 1000; return &[]MemoryFile{f} }

	for name, tc := range map[string]struct {
		audit KnowledgeAudit
		files *[]MemoryFile
		boss  bool
		want  string // "" passes
	}{
		"worker without action":         {bare, small(MemoryFile{Deleted: 4}), false, ""},
		"boss without action":           {bare, nil, true, "没声明动作"},
		"boss update deleting lines":    {update, small(MemoryFile{Deleted: 4}), true, ""},
		"boss new deleting lines":       {KnowledgeAudit{Changes: []KnowledgeChange{{Location: "agents", Action: ActionNew, Summary: "s"}}}, small(MemoryFile{Deleted: 2}), true, "不悄悄删"},
		"supersede without mark":        {supersede, small(MemoryFile{Deleted: 1}), false, "已被 X 取代"},
		"supersede with mark":           {supersede, small(MemoryFile{Deleted: 1, SupersedeMarks: 1}), true, ""},
		"boss with no facts":            {update, nil, true, ""},
		"boss none deleting lines":      {KnowledgeAudit{None: true}, small(MemoryFile{Deleted: 3}), true, "--knowledge-none"},
		"worker none deleting lines":    {KnowledgeAudit{None: true}, small(MemoryFile{Deleted: 3}), false, ""},
		"none still checks size":        {KnowledgeAudit{None: true}, &[]MemoryFile{{Path: "CONTEXT.md", Bytes: MapFileMaxBytes + 1}}, false, "超过地图文件上限"},
		"adr files have no size limit":  {bare, &[]MemoryFile{{Path: "docs/adr/0001-x.md", Bytes: MapFileMaxBytes * 2}}, false, ""},
		"nested AGENTS.md is a map too": {bare, &[]MemoryFile{{Path: "apps/x/AGENTS.md", Bytes: MapFileMaxBytes + 10}}, false, "apps/x/AGENTS.md"},
	} {
		err := CheckMemoryHygiene(tc.audit, tc.files, tc.boss)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected refusal %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestMapFileOverLimitNamesWhatToMerge(t *testing.T) {
	files := []MemoryFile{{
		Path:  "AGENTS.md",
		Bytes: MapFileMaxBytes + 5000,
		Sections: []MemorySection{
			{Heading: "开头", Bytes: 800},
			{Heading: "旧派单", Bytes: 1200, Superseded: true},
			{Heading: "工作单", Bytes: 9000},
			{Heading: "状态规则", Bytes: 3000},
		},
	}}
	err := CheckMemoryHygiene(KnowledgeAudit{None: true}, &files, false)
	if err == nil {
		t.Fatal("an oversized map file passed")
	}
	msg := err.Error()
	for _, want := range []string{"AGENTS.md", "多 5000", "先删已标取代的 「旧派单」", "「工作单」(9000 字节)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal lacks %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "状态规则") {
		t.Errorf("refusal names more sections than the overflow needs: %s", msg)
	}
}
