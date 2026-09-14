package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func setPasswordAuthEnv(t *testing.T, username, password, email string) {
	t.Helper()
	t.Setenv(passwordAuthEnabledEnv, "true")
	t.Setenv(passwordAuthUsernameEnv, username)
	t.Setenv(passwordAuthPasswordEnv, password)
	t.Setenv(passwordAuthEmailEnv, email)
}

func postPasswordLogin(username, password string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{
		"username": username,
		"password": password,
	})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	testHandler.PasswordLogin(w, req)
	return w
}

func TestPasswordAuthConfiguredRequiresAllFields(t *testing.T) {
	t.Setenv(passwordAuthEnabledEnv, "")
	t.Setenv(passwordAuthUsernameEnv, "")
	t.Setenv(passwordAuthPasswordEnv, "")
	t.Setenv(passwordAuthEmailEnv, "")
	if _, ok := passwordAuthConfigured(); ok {
		t.Fatal("password auth must stay off by default")
	}

	t.Setenv(passwordAuthEnabledEnv, "true")
	t.Setenv(passwordAuthUsernameEnv, "kun")
	t.Setenv(passwordAuthPasswordEnv, "")
	t.Setenv(passwordAuthEmailEnv, "kun@example.com")
	if _, ok := passwordAuthConfigured(); ok {
		t.Fatal("password auth must stay off when the password is missing")
	}

	setPasswordAuthEnv(t, "kun", "s3cret", "kun@example.com")
	creds, ok := passwordAuthConfigured()
	if !ok {
		t.Fatal("password auth should be on when all fields are set")
	}
	if creds.username != "kun" || creds.email != "kun@example.com" {
		t.Fatalf("unexpected creds: %+v", creds)
	}
}

func TestPasswordLoginDisabled(t *testing.T) {
	t.Setenv(passwordAuthEnabledEnv, "")
	w := postPasswordLogin("kun", "s3cret")
	if w.Code != http.StatusNotFound {
		t.Fatalf("PasswordLogin (disabled): expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPasswordLoginWrongPassword(t *testing.T) {
	const email = "password-wrong-test@multica.ai"
	setPasswordAuthEnv(t, "kun", "correct-horse", email)

	w := postPasswordLogin("kun", "wrong-password")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("PasswordLogin (wrong password): expected 401, got %d: %s", w.Code, w.Body.String())
	}

	w = postPasswordLogin("other", "correct-horse")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("PasswordLogin (wrong username): expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPasswordLoginSuccessIssuesJWT(t *testing.T) {
	const (
		email    = "password-login-test@multica.ai"
		username = "kun"
		password = "correct-horse-battery"
	)
	setPasswordAuthEnv(t, username, password, email)
	ctx := context.Background()

	t.Cleanup(func() {
		user, err := testHandler.Queries.GetUserByEmail(ctx, email)
		if err == nil {
			workspaces, listErr := testHandler.Queries.ListWorkspaces(ctx, user.ID)
			if listErr == nil {
				for _, workspace := range workspaces {
					_ = testHandler.Queries.DeleteWorkspace(ctx, workspace.ID)
				}
			}
		}
		testPool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, email)
	})

	w := postPasswordLogin(username, password)
	if w.Code != http.StatusOK {
		t.Fatalf("PasswordLogin: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp LoginResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("PasswordLogin: expected non-empty token")
	}
	if resp.User.Email != email {
		t.Fatalf("PasswordLogin: expected email %q, got %q", email, resp.User.Email)
	}

	foundAuthCookie := false
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "multica_auth" && cookie.Value != "" {
			foundAuthCookie = true
			break
		}
	}
	if !foundAuthCookie {
		t.Fatal("PasswordLogin: expected HttpOnly multica_auth cookie on success")
	}

	w = postPasswordLogin(username, password)
	if w.Code != http.StatusOK {
		t.Fatalf("PasswordLogin (existing user): expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSendCodeRejectedWhenPasswordAuthEnabled(t *testing.T) {
	setPasswordAuthEnv(t, "kun", "s3cret", "password-send-blocked@multica.ai")

	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]string{"email": "password-send-blocked@multica.ai"})
	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	testHandler.SendCode(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("SendCode with password auth: expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetConfigExposesPasswordAuth(t *testing.T) {
	t.Setenv(passwordAuthEnabledEnv, "")
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	w := httptest.NewRecorder()
	testHandler.GetConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetConfig: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var off AppConfig
	if err := json.Unmarshal(w.Body.Bytes(), &off); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if off.PasswordAuth {
		t.Fatal("password_auth: want omitted/false by default")
	}

	setPasswordAuthEnv(t, "kun", "s3cret", "kun@example.com")
	w = httptest.NewRecorder()
	testHandler.GetConfig(w, req)
	var on AppConfig
	if err := json.Unmarshal(w.Body.Bytes(), &on); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if !on.PasswordAuth {
		t.Fatal("password_auth: want true when fully configured")
	}
}
