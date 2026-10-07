package routing

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The case table is shared with the clients (DENE-1477): a rule change that
// does not update it fails here first.
type domainFitTable struct {
	Scenes []struct {
		Name           string   `json:"name"`
		IssueDomain    string   `json:"issue_domain"`
		ProjectDomains []string `json:"project_domains"`
		Want           []string `json:"want"`
	} `json:"scenes"`
	Cases []struct {
		Name  string   `json:"name"`
		Scene []string `json:"scene"`
		Agent struct {
			Name   string `json:"name"`
			Domain string `json:"domain"`
		} `json:"agent"`
		Fit Fit `json:"fit"`
	} `json:"cases"`
}

func loadDomainFitTable(t *testing.T) domainFitTable {
	t.Helper()
	raw, err := os.ReadFile("../../../packages/core/agents/domain-fit.cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	var table domainFitTable
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Scenes) == 0 || len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table
}

func TestDomainFitCaseTable(t *testing.T) {
	table := loadDomainFitTable(t)
	for _, tc := range table.Scenes {
		got := ResolveScene(tc.IssueDomain, tc.ProjectDomains).Domains
		if len(got) == 0 && len(tc.Want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.Want) {
			t.Errorf("scene %q: got %v, want %v", tc.Name, got, tc.Want)
		}
	}
	for _, tc := range table.Cases {
		scene := Scene{Domains: tc.Scene}
		if got := DomainFit(scene, tc.Agent.Domain); got != tc.Fit {
			t.Errorf("fit %q: DomainFit = %s, want %s", tc.Name, got, tc.Fit)
		}
		// The routing seat path agrees with the pure rule.
		a := Agent{Name: tc.Agent.Name, Direction: tc.Agent.Domain}
		if got := DefaultLadder.AgentFit(scene, a); got != tc.Fit {
			t.Errorf("fit %q: AgentFit = %s, want %s", tc.Name, got, tc.Fit)
		}
	}
}

func TestNameSuffixOnlyCountsForAnAgentWithNoRecordedDomain(t *testing.T) {
	l := DefaultLadder
	overseas := SceneOf("出海")
	// A legacy specialisation with no recorded domain is read off its name.
	if got := l.AgentFit(overseas, Agent{Name: "孙悟空出海"}); got != FitMatch {
		t.Errorf("legacy name suffix: got %s, want match", got)
	}
	// A recorded domain decides over whatever the name says.
	if got := l.AgentFit(overseas, Agent{Name: "孙悟空出海", Direction: "游戏"}); got != FitOther {
		t.Errorf("recorded domain must win over the name: got %s, want other", got)
	}
}

func TestFitRanksMatchGenericOther(t *testing.T) {
	if !(FitMatch.Rank() < FitGeneric.Rank() && FitGeneric.Rank() < FitOther.Rank()) {
		t.Fatal("fit groups must order match < generic < other")
	}
	if FitMatch.Label() != "对口" || FitGeneric.Label() != "通用" || FitOther.Label() != "其他" {
		t.Fatal("fit labels drifted from the product words")
	}
}
