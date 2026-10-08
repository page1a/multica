package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/localoutputs"
)

const localOutputTestID = "01a11579-ea96-76ba-8a38-13dba24d52ce"

func newLocalOutputsTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := &Daemon{localOutputs: &localoutputs.Store{Root: filepath.Join(t.TempDir(), "outputs")}}
	d.registerActiveRepoCheckoutTask("task-token", activeRepoCheckoutTask{})
	return d
}

func serveLocalOutputs(d *Daemon, method, query, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/outputs?"+query, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	d.localOutputsHandler().ServeHTTP(rec, req)
	return rec
}

func TestLocalOutputsHandlerRoundTrip(t *testing.T) {
	d := newLocalOutputsTestDaemon(t)
	q := "attachment_id=" + localOutputTestID + "&filename=report.html"
	if rec := serveLocalOutputs(d, http.MethodPost, q, "task-token", "<p>hi</p>"); rec.Code != http.StatusOK {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body.String())
	}
	rec := serveLocalOutputs(d, http.MethodGet, "attachment_id="+localOutputTestID, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Filename string `json:"filename"`
		Path     string `json:"path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(got.Path)
	if err != nil || string(data) != "<p>hi</p>" || got.Filename != "report.html" {
		t.Fatalf("copy = %q (%v), filename %q", data, err, got.Filename)
	}

	if err := os.Remove(got.Path); err != nil {
		t.Fatal(err)
	}
	if rec := serveLocalOutputs(d, http.MethodGet, "attachment_id="+localOutputTestID, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET after delete = %d, want 404", rec.Code)
	}
}

func TestLocalOutputsHandlerRejectsWritesWithoutTaskToken(t *testing.T) {
	d := newLocalOutputsTestDaemon(t)
	q := "attachment_id=" + localOutputTestID + "&filename=a.txt"
	for _, token := range []string{"", "someone-else"} {
		if rec := serveLocalOutputs(d, http.MethodPost, q, token, "x"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("POST with token %q = %d, want 401", token, rec.Code)
		}
	}
}

func TestLocalOutputsHandlerRejectsPathsAsIDs(t *testing.T) {
	d := newLocalOutputsTestDaemon(t)
	for _, id := range []string{"", "..%2F..%2Fetc%2Fpasswd", "%2Fetc%2Fpasswd", "abc"} {
		if rec := serveLocalOutputs(d, http.MethodGet, "attachment_id="+id, "", ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("GET id %q = %d, want 400", id, rec.Code)
		}
	}
}
