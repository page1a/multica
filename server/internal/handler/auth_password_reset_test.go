package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
	"golang.org/x/crypto/bcrypt"
)

func postPasswordReset(username, totp, newPassword string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(PasswordResetRequest{Username: username, Totp: totp, NewPassword: newPassword})
	req := httptest.NewRequest(http.MethodPost, "/auth/reset-password", &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	testHandler.PasswordReset(w, req)
	return w
}

// passwordUser inserts a user with a password credential and returns the user
// id and the (lowercase) username.
func passwordUser(t *testing.T, password string) (string, string) {
	t.Helper()
	username := "reset" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	userID := dbfx.User(t, username, username+"@signup.invalid")
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := testPool.Exec(t.Context(),
		`INSERT INTO user_password_credential (user_id, username, password_hash) VALUES ($1, $2, $3)`,
		parseUUID(userID), username, string(hash),
	); err != nil {
		t.Fatalf("insert credential: %v", err)
	}
	return userID, username
}

func enablePasswordReset(t *testing.T) []byte {
	t.Helper()
	setPasswordAuthEnv(t, "kun", "s3cret-bootstrap", "password-auth-bootstrap@multica.ai")
	t.Setenv(signupTOTPSecretEnv, rfc6238TOTPSecretBase32)
	secret := rfc6238TOTPSecret(t)
	cleanupSignupTOTPUsedSteps(t, secret)
	return secret
}

func resetAuditCount(t *testing.T, userID, method string) int {
	t.Helper()
	return dbfx.Count(t, `SELECT count(*) FROM password_reset_audit WHERE user_id = $1 AND method = $2`, parseUUID(userID), method)
}

func TestPasswordResetSelfServiceFlow(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	secret := enablePasswordReset(t)
	userID, username := passwordUser(t, "old-password-1")

	code := totpCodeAt(secret, time.Now().Unix())
	if w := postPasswordReset(strings.ToUpper(username), code, "new-password-2"); w.Code != http.StatusNoContent {
		t.Fatalf("reset: expected 204, got %d: %s", w.Code, w.Body.String())
	}
	if w := postPasswordLogin(username, "old-password-1"); w.Code != http.StatusUnauthorized {
		t.Fatalf("old password: expected 401, got %d", w.Code)
	}
	if w := postPasswordLogin(username, "new-password-2"); w.Code != http.StatusOK {
		t.Fatalf("new password: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if n := resetAuditCount(t, userID, passwordResetMethodSelf); n != 1 {
		t.Fatalf("self audit rows: got %d, want 1", n)
	}

	// The same code cannot be used twice.
	if w := postPasswordReset(username, code, "third-password-3"); w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed code: expected 401, got %d", w.Code)
	}
	if w := postPasswordLogin(username, "new-password-2"); w.Code != http.StatusOK {
		t.Fatalf("replay must not change the password, login got %d", w.Code)
	}
}

func TestPasswordResetRejectionsLookAlike(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	secret := enablePasswordReset(t)
	userID, username := passwordUser(t, "old-password-1")
	good := totpCodeAt(secret, time.Now().Unix())
	bad := "000000"
	if bad == good {
		bad = "111111"
	}

	cases := map[string]*httptest.ResponseRecorder{
		"wrong code":       postPasswordReset(username, bad, "new-password-2"),
		"unknown username": postPasswordReset("nobody-"+uuid.NewString()[:8], good, "new-password-2"),
		"bootstrap user":   postPasswordReset("kun", good, "new-password-2"),
	}
	for name, w := range cases {
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401, got %d: %s", name, w.Code, w.Body.String())
		}
		if msg := signupErrorMessage(t, w); msg != passwordResetRejectedMessage {
			t.Fatalf("%s: message %q leaks which part was wrong", name, msg)
		}
	}
	if w := postPasswordLogin(username, "old-password-1"); w.Code != http.StatusOK {
		t.Fatalf("rejected resets must not change the password, login got %d", w.Code)
	}
	if n := resetAuditCount(t, userID, passwordResetMethodSelf); n != 0 {
		t.Fatalf("rejected resets wrote %d audit rows", n)
	}

	if w := postPasswordReset(username, good, "short"); w.Code != http.StatusBadRequest {
		t.Fatalf("short password: expected 400, got %d", w.Code)
	}
}

func TestPasswordResetDisabledWithoutTeam2FA(t *testing.T) {
	setPasswordAuthEnv(t, "kun", "s3cret-bootstrap", "password-auth-bootstrap@multica.ai")
	t.Setenv(signupTOTPSecretEnv, "")
	if w := postPasswordReset("anyone", "123456", "new-password-2"); w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 without team 2FA, got %d", w.Code)
	}
}

func resetMemberAs(t *testing.T, actorID, memberID string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequest("POST", "/api/workspaces/"+testWorkspaceID+"/members/"+memberID+"/reset-password", nil)
	req.Header.Set("X-User-ID", actorID)
	for _, m := range mutate {
		m(req)
	}
	req = testutil.WithURLParams(req, "id", testWorkspaceID, "memberId", memberID)
	w := httptest.NewRecorder()
	testHandler.ResetMemberPassword(w, req)
	return w
}

func TestResetMemberPasswordByOwner(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setPasswordAuthEnv(t, "kun", "s3cret-bootstrap", "password-auth-bootstrap@multica.ai")
	userID, username := passwordUser(t, "old-password-1")
	memberID := dbfx.Member(t, testWorkspaceID, userID, "member")

	w := resetMemberAs(t, testUserID, memberID)
	if w.Code != http.StatusOK {
		t.Fatalf("owner reset: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp MemberPasswordResetResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Username != username || len(resp.TemporaryPassword) != tempPasswordLen {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if w := postPasswordLogin(username, resp.TemporaryPassword); w.Code != http.StatusOK {
		t.Fatalf("temporary password login: expected 200, got %d", w.Code)
	}
	if w := postPasswordLogin(username, "old-password-1"); w.Code != http.StatusUnauthorized {
		t.Fatalf("old password: expected 401, got %d", w.Code)
	}
	if n := dbfx.Count(t,
		`SELECT count(*) FROM password_reset_audit WHERE user_id = $1 AND method = 'admin' AND actor_user_id = $2 AND workspace_id = $3`,
		parseUUID(userID), parseUUID(testUserID), parseUUID(testWorkspaceID),
	); n != 1 {
		t.Fatalf("admin audit rows: got %d, want 1", n)
	}
}

func TestResetMemberPasswordByAdmin(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setPasswordAuthEnv(t, "kun", "s3cret-bootstrap", "password-auth-bootstrap@multica.ai")
	adminID := dbfx.User(t, "Reset Admin", "reset-admin-"+uuid.NewString()+"@multica.ai")
	dbfx.Member(t, testWorkspaceID, adminID, "admin")
	userID, username := passwordUser(t, "old-password-1")
	memberID := dbfx.Member(t, testWorkspaceID, userID, "member")

	w := resetMemberAs(t, adminID, memberID)
	if w.Code != http.StatusOK {
		t.Fatalf("admin reset: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp MemberPasswordResetResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if w := postPasswordLogin(username, resp.TemporaryPassword); w.Code != http.StatusOK {
		t.Fatalf("temporary password login: expected 200, got %d", w.Code)
	}
	if n := dbfx.Count(t,
		`SELECT count(*) FROM password_reset_audit WHERE user_id = $1 AND method = 'admin' AND actor_user_id = $2`,
		parseUUID(userID), parseUUID(adminID),
	); n != 1 {
		t.Fatalf("admin audit rows: got %d, want 1", n)
	}
}

func TestResetMemberPasswordRefusals(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	setPasswordAuthEnv(t, "kun", "s3cret-bootstrap", "password-auth-bootstrap@multica.ai")

	t.Run("plain member", func(t *testing.T) {
		actorID := dbfx.User(t, "Reset Member", "reset-member-"+uuid.NewString()+"@multica.ai")
		dbfx.Member(t, testWorkspaceID, actorID, "member")
		userID, _ := passwordUser(t, "old-password-1")
		memberID := dbfx.Member(t, testWorkspaceID, userID, "member")
		if w := resetMemberAs(t, actorID, memberID); w.Code != http.StatusForbidden {
			t.Fatalf("member: expected 403, got %d", w.Code)
		}
	})

	t.Run("admin cannot reset an admin or the owner", func(t *testing.T) {
		adminID := dbfx.User(t, "Reset Admin", "reset-admin-"+uuid.NewString()+"@multica.ai")
		dbfx.Member(t, testWorkspaceID, adminID, "admin")
		peerID, _ := passwordUser(t, "old-password-1")
		peerMemberID := dbfx.Member(t, testWorkspaceID, peerID, "admin")
		if w := resetMemberAs(t, adminID, peerMemberID); w.Code != http.StatusForbidden {
			t.Fatalf("admin -> admin: expected 403, got %d: %s", w.Code, w.Body.String())
		}
		var ownerMemberID string
		dbfx.QueryRow(t, `SELECT id::text FROM member WHERE workspace_id = $1 AND user_id = $2`,
			parseUUID(testWorkspaceID), parseUUID(testUserID)).Scan(&ownerMemberID)
		if w := resetMemberAs(t, adminID, ownerMemberID); w.Code != http.StatusForbidden {
			t.Fatalf("admin -> owner: expected 403, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("agent task token", func(t *testing.T) {
		userID, _ := passwordUser(t, "old-password-1")
		memberID := dbfx.Member(t, testWorkspaceID, userID, "member")
		w := resetMemberAs(t, testUserID, memberID, func(r *http.Request) { r.Header.Set("X-Actor-Source", "task_token") })
		if w.Code != http.StatusForbidden {
			t.Fatalf("task token: expected 403, got %d", w.Code)
		}
	})

	t.Run("admin of a workspace the owner does not own", func(t *testing.T) {
		userID, _ := passwordUser(t, "old-password-1")
		memberID := dbfx.Member(t, testWorkspaceID, userID, "member")
		otherWS := dbfx.Workspace(t, "Other", "reset-other-"+uuid.NewString()[:8])
		dbfx.Member(t, otherWS, userID, "admin")
		if w := resetMemberAs(t, testUserID, memberID); w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("no password credential", func(t *testing.T) {
		userID := dbfx.User(t, "Email Only", "email-only-"+uuid.NewString()+"@multica.ai")
		memberID := dbfx.Member(t, testWorkspaceID, userID, "member")
		if w := resetMemberAs(t, testUserID, memberID); w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", w.Code)
		}
	})

	t.Run("self", func(t *testing.T) {
		var ownerMemberID string
		dbfx.QueryRow(t, `SELECT id::text FROM member WHERE workspace_id = $1 AND user_id = $2`,
			parseUUID(testWorkspaceID), parseUUID(testUserID)).Scan(&ownerMemberID)
		if w := resetMemberAs(t, testUserID, ownerMemberID); w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", w.Code)
		}
	})
}
