package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// DENE-1022 through the real router: the integration user is made an admin /
// member / guest of a scratch workspace that also has an owner, and every
// member-management route must refuse them while the roster stays readable
// without management fields.
func TestMemberManagementIsOwnerOnlyThroughRouter(t *testing.T) {
	ctx := context.Background()
	for _, role := range []string{"admin", "member", "guest"} {
		t.Run(role, func(t *testing.T) {
			slug := "dene1022-" + role + "-" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
			var wsID string
			if err := testPool.QueryRow(ctx, `INSERT INTO workspace (name, slug, description) VALUES ($1, $2, '') RETURNING id`, "DENE-1022 "+role, slug).Scan(&wsID); err != nil {
				t.Fatalf("create workspace: %v", err)
			}
			var ownerID string
			ownerEmail := "dene1022-owner-" + slug + "@multica.ai"
			if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('DENE1022 Owner', $1) RETURNING id`, ownerEmail).Scan(&ownerID); err != nil {
				t.Fatalf("create owner user: %v", err)
			}
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, wsID)
				_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, ownerID)
			})
			var ownerMemberID string
			if err := testPool.QueryRow(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner') RETURNING id`, wsID, ownerID).Scan(&ownerMemberID); err != nil {
				t.Fatalf("create owner member: %v", err)
			}
			if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, $3)`, wsID, testUserID, role); err != nil {
				t.Fatalf("create %s member: %v", role, err)
			}

			do := func(method, path string, body any) int {
				t.Helper()
				var reader io.Reader
				if body != nil {
					b, _ := json.Marshal(body)
					reader = bytes.NewReader(b)
				}
				req, err := http.NewRequest(method, testServer.URL+"/api/workspaces/"+wsID+path, reader)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+testToken)
				req.Header.Set("X-Workspace-ID", wsID)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				return resp.StatusCode
			}

			for name, code := range map[string]int{
				"list invitations":  do("GET", "/invitations", nil),
				"invite":            do("POST", "/members", map[string]any{"email": "x-" + slug + "@multica.ai", "role": "member"}),
				"change tier":       do("PATCH", "/members/"+ownerMemberID, map[string]any{"role": "member"}),
				"remove member":     do("DELETE", "/members/"+ownerMemberID, nil),
				"revoke invitation": do("DELETE", "/invitations/"+uuid.NewString(), nil),
				"create share link": do("POST", "/share-links", map[string]any{"role": "member"}),
				"list share links":  do("GET", "/share-links", nil),
			} {
				if code != http.StatusForbidden {
					t.Errorf("%s as %s: status = %d, want 403", name, role, code)
				}
			}

			// The roster is still readable; management fields are not.
			req, _ := http.NewRequest("GET", testServer.URL+"/api/workspaces/"+wsID+"/members", nil)
			req.Header.Set("Authorization", "Bearer "+testToken)
			req.Header.Set("X-Workspace-ID", wsID)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("roster as %s: status = %d, want 200", role, resp.StatusCode)
			}
			var roster []map[string]any
			readJSON(t, resp, &roster)
			for _, row := range roster {
				if row["user_id"] == ownerID {
					if row["name"] != "DENE1022 Owner" {
						t.Errorf("owner row lost its name: %v", row)
					}
					if row["email"] != "" || row["role"] != "" || row["created_at"] != "" {
						t.Errorf("%s sees owner management fields: %v", role, row)
					}
				}
				if row["user_id"] == testUserID && row["role"] != role {
					t.Errorf("own row role = %v, want %s", row["role"], role)
				}
			}
		})
	}
}
