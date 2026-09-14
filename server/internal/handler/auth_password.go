package handler

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
)

const (
	passwordAuthEnabledEnv  = "MULTICA_PASSWORD_AUTH"
	passwordAuthUsernameEnv = "MULTICA_PASSWORD_AUTH_USERNAME"
	passwordAuthPasswordEnv = "MULTICA_PASSWORD_AUTH_PASSWORD"
	passwordAuthEmailEnv    = "MULTICA_PASSWORD_AUTH_EMAIL"

	maxPasswordLoginUsernameLen = 256
	maxPasswordLoginPasswordLen = 1024
)

type passwordAuthCreds struct {
	username string
	password string
	email    string
}

var passwordAuthMisconfigOnce sync.Once

func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func secretEqual(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// passwordAuthConfigured reports whether this process should serve username
// + password login. All four env vars must be present; a half-set switch
// stays off so a typo cannot lock operators out of email login.
func passwordAuthConfigured() (passwordAuthCreds, bool) {
	if !envTruthy(os.Getenv(passwordAuthEnabledEnv)) {
		return passwordAuthCreds{}, false
	}

	username := strings.TrimSpace(os.Getenv(passwordAuthUsernameEnv))
	password := strings.TrimSpace(os.Getenv(passwordAuthPasswordEnv))
	email := strings.ToLower(strings.TrimSpace(os.Getenv(passwordAuthEmailEnv)))
	if username == "" || password == "" || !strings.Contains(email, "@") {
		passwordAuthMisconfigOnce.Do(func() {
			slog.Warn("MULTICA_PASSWORD_AUTH is set but username, password, or email is incomplete; password login stays disabled")
		})
		return passwordAuthCreds{}, false
	}
	return passwordAuthCreds{username: username, password: password, email: email}, true
}

func rejectEmailLoginIfPasswordAuth(w http.ResponseWriter) bool {
	if _, ok := passwordAuthConfigured(); !ok {
		return false
	}
	writeError(w, http.StatusForbidden, "email verification login is disabled")
	return true
}

type PasswordLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *Handler) PasswordLogin(w http.ResponseWriter, r *http.Request) {
	creds, ok := passwordAuthConfigured()
	if !ok {
		writeError(w, http.StatusNotFound, "password login is not enabled")
		return
	}

	var req PasswordLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	username := strings.TrimSpace(req.Username)
	password := strings.TrimSpace(req.Password)
	if username == "" || password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	if len(username) > maxPasswordLoginUsernameLen || len(password) > maxPasswordLoginPasswordLen {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	userOK := secretEqual(username, creds.username)
	passOK := secretEqual(password, creds.password)
	if !userOK || !passOK {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	user, isNew, err := h.findOrCreateUser(r.Context(), creds.email)
	if err != nil {
		if errors.Is(err, auth.ErrTemporarilyDisabledUser) {
			writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
			return
		}
		var signupErr SignupError
		if errors.As(err, &signupErr) {
			writeError(w, http.StatusForbidden, signupErr.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}
	if isNew {
		obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.Signup(uuidToString(user.ID), user.Email, signupSourceFromRequest(r)))
	}

	tokenString, err := h.issueJWT(user)
	if err != nil {
		if errors.Is(err, auth.ErrTemporarilyDisabledUser) {
			writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
			return
		}
		slog.Warn("password login failed", append(logger.RequestAttrs(r), "error", err, "email", creds.email)...)
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	if err := auth.SetAuthCookies(w, tokenString); err != nil {
		slog.Warn("failed to set auth cookies", "error", err)
	}

	if h.CFSigner != nil {
		for _, cookie := range h.CFSigner.SignedCookies(time.Now().Add(auth.AuthTokenTTL())) {
			http.SetCookie(w, cookie)
		}
	}

	slog.Info("user logged in via password", append(logger.RequestAttrs(r), "user_id", uuidToString(user.ID), "email", user.Email)...)
	writeJSON(w, http.StatusOK, LoginResponse{
		Token: tokenString,
		User:  h.userToResponse(user),
	})
}
