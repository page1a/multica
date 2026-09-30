package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/projectmemory"
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
