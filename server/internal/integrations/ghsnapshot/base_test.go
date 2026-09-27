package ghsnapshot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFetchBaseChecksNamesRedChecksOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"ghs_x","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":{"baseRefName":"kun","baseRef":{"target":{"oid":"base1","statusCheckRollup":{"state":"FAILURE","contexts":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
			{"__typename":"CheckRun","name":"frontend-test","status":"COMPLETED","conclusion":"FAILURE"},
			{"__typename":"CheckRun","name":"backend-tests","status":"COMPLETED","conclusion":"SKIPPED"},
			{"__typename":"CheckRun","name":"backend-tests","status":"COMPLETED","conclusion":"FAILURE"},
			{"__typename":"CheckRun","name":"frontend-build","status":"IN_PROGRESS","conclusion":null},
			{"__typename":"CheckRun","name":"sqlc-check","status":"COMPLETED","conclusion":"SUCCESS"},
			{"__typename":"StatusContext","context":"legacy","state":"ERROR"}
		]}}}}}}}}`))
	}))
	defer srv.Close()

	base, err := FetchBaseChecks(context.Background(), newTestClient(t, srv.URL), 1, "o", "r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if base.Branch != "kun" || base.HeadSHA != "base1" {
		t.Fatalf("base = %+v", base)
	}
	want := []string{"backend-tests", "frontend-test", "legacy"}
	if got := base.FailedNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("failed = %v, want %v", got, want)
	}
}
