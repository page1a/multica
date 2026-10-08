package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestIncrementalCursorRoundTrip(t *testing.T) {
	want := incrementalCursor{At: time.Date(2026, 10, 4, 1, 2, 3, 456000000, time.UTC), ID: "11111111-1111-1111-1111-111111111111"}
	got, err := decodeIncrementalCursor(encodeIncrementalCursor(want))
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if !got.At.Equal(want.At) || got.ID != want.ID {
		t.Fatalf("cursor = %#v, want %#v", got, want)
	}
}

func TestIncrementalCursorRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{"", "not-base64", encodeIncrementalCursor(incrementalCursor{ID: "x"})} {
		if _, err := decodeIncrementalCursor(value); err == nil {
			t.Fatalf("decode %q unexpectedly succeeded", value)
		}
	}
}

// Exercise the actual UNION query against PostgreSQL: cursor-only tests cannot
// catch a missing output alias used by ORDER BY.
func TestIncrementalInboxChanges(t *testing.T) {
	workspaceID := dbfx.Workspace(t, "Inbox sync", "inbox-sync-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	insertItem := func(created time.Time, read any) string {
		return dbfx.Insert(t, "inbox_item", testutil.Cols{
			"workspace_id": workspaceID, "recipient_type": "member", "recipient_id": testUserID,
			"type": "status_changed", "severity": "info", "title": "Sync regression",
			"created_at": created, "read_at": read,
		})
	}
	unreadID := insertItem(at, nil)
	readID := insertItem(at.Add(-time.Minute), at.Add(time.Minute))
	deletedID := uuid.NewString()
	dbfx.Insert(t, "incremental_sync_tombstone", testutil.Cols{
		"id": deletedID, "resource": "inbox", "workspace_id": workspaceID,
		"subject_id": testUserID, "changed_at": at.Add(2 * time.Minute),
	})
	type page struct {
		Upserts    []IncrementalChange `json:"upserts"`
		Deleted    []string            `json:"deleted"`
		NextCursor string              `json:"next_cursor"`
		HasMore    bool                `json:"has_more"`
	}
	fetch := func(cursor string) page {
		var result page
		testutil.Call(t, inboxWorkspaceHandler(testHandler.ListIncrementalChanges),
			inboxRequest(http.MethodGet, "/api/sync/changes?resource=inbox&limit=1&cursor="+cursor, workspaceID)).
			Want(http.StatusOK).JSON(&result)
		return result
	}
	cursor := ""
	for i, want := range []struct {
		id      string
		at      time.Time
		deleted bool
	}{
		{unreadID, at, false}, {readID, at.Add(time.Minute), false}, {deletedID, at.Add(2 * time.Minute), true},
	} {
		got := fetch(cursor)
		if got.HasMore != (i < 2) {
			t.Fatalf("page %d has_more = %v", i, got.HasMore)
		}
		if want.deleted {
			if len(got.Upserts) != 0 || len(got.Deleted) != 1 || got.Deleted[0] != want.id {
				t.Fatalf("deleted page = %+v", got)
			}
		} else if len(got.Deleted) != 0 || len(got.Upserts) != 1 || got.Upserts[0].ID != want.id || !got.Upserts[0].UpdatedAt.Equal(want.at) {
			t.Fatalf("upsert page %d = %+v", i, got)
		}
		decoded, err := decodeIncrementalCursor(got.NextCursor)
		if err != nil || decoded.ID != want.id || !decoded.At.Equal(want.at) {
			t.Fatalf("page %d cursor = %+v, %v", i, decoded, err)
		}
		cursor = got.NextCursor
	}
	if got := fetch(cursor); len(got.Upserts) != 0 || len(got.Deleted) != 0 || got.HasMore || got.NextCursor != cursor {
		t.Fatalf("exhausted page = %+v", got)
	}
}
