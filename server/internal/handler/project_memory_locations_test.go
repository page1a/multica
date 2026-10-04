package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/projectmemory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestListProjectMemoryLocationsUsesTheChecklist(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/project-memory/locations", nil)
	(&Handler{}).ListProjectMemoryLocations(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Locations []projectmemory.Location `json:"locations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := projectmemory.Locations()
	if len(resp.Locations) != len(want) {
		t.Fatalf("locations = %#v", resp.Locations)
	}
	for i := range want {
		if resp.Locations[i] != want[i] {
			t.Fatalf("locations[%d] = %#v, want %#v", i, resp.Locations[i], want[i])
		}
	}
}

func TestSedimentInstructionFallsBackToBuiltin(t *testing.T) {
	cases := map[string]string{
		``:                                  SedimentBuiltinInstruction,
		`{"memory":{"sediment_agent":"x"}}`: SedimentBuiltinInstruction,
		`{"memory":{"sediment_instruction":"   "}}`:              SedimentBuiltinInstruction,
		`{"memory":{"sediment_instruction":"  先读 AGENTS.md  "}}`: "先读 AGENTS.md",
	}
	for settings, want := range cases {
		if got := sedimentInstruction([]byte(settings)); got != want {
			t.Errorf("sedimentInstruction(%q) = %q, want %q", settings, got, want)
		}
	}
}

func TestValidateWorkspaceMemorySettingsBoundsInstruction(t *testing.T) {
	long := strings.Repeat("字", maxSedimentInstructionBytes/3+1)
	settings := map[string]any{"memory": map[string]any{"sediment_instruction": long}}
	if err := validateWorkspaceMemorySettings(context.Background(), &db.Queries{}, pgtype.UUID{}, settings); err == nil {
		t.Fatal("over-long sediment_instruction was accepted")
	}
	settings = map[string]any{"memory": map[string]any{"sediment_instruction": "先读 AGENTS.md"}}
	if err := validateWorkspaceMemorySettings(context.Background(), &db.Queries{}, pgtype.UUID{}, settings); err != nil {
		t.Fatalf("short sediment_instruction rejected: %v", err)
	}
}
