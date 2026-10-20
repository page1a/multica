package handler

import (
	"context"
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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"golang.org/x/crypto/bcrypt"
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
	if _, ok := passwordAuthConfigured(); !ok {
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

	user, isNew, err := h.authenticatePasswordUser(r.Context(), username, password)
	if err != nil {
		if errors.Is(err, errInvalidPasswordCreds) {
			writeError(w, http.StatusUnauthorized, "invalid username or password")
			return
		}
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
		slog.Warn("password login failed", append(logger.RequestAttrs(r), "error", err, "email", user.Email)...)
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

var errInvalidPasswordCreds = errors.New("invalid username or password")

// authenticatePasswordUser checks the credential table first, then the single
// env-var operator. A stored username always wins, so it cannot be shadowed by
// MULTICA_PASSWORD_AUTH_USERNAME.
func (h *Handler) authenticatePasswordUser(ctx context.Context, username, password string) (db.User, bool, error) {
	rec, err := h.Queries.GetPasswordCredentialByUsername(ctx, username)
	switch {
	case err == nil:
		if bcrypt.CompareHashAndPassword([]byte(rec.PasswordHash), []byte(password)) != nil {
			return db.User{}, false, errInvalidPasswordCreds
		}
		user, err := h.Queries.GetUser(ctx, rec.UserID)
		if err != nil {
			return db.User{}, false, errInvalidPasswordCreds
		}
		if auth.IsTemporarilyDisabledUser(uuidToString(user.ID), user.Email) {
			return db.User{}, false, auth.ErrTemporarilyDisabledUser
		}
		return user, false, nil
	case errors.Is(err, pgx.ErrNoRows):
		// no stored account for this username; fall through
	default:
		// A missing table (migration not applied) must not take password login
		// down with it. Anything else is a real database failure.
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42P01" {
			return db.User{}, false, err
		}
	}

	creds, ok := passwordAuthConfigured()
	if !ok || !secretEqual(username, creds.username) || !secretEqual(password, creds.password) {
		return db.User{}, false, errInvalidPasswordCreds
	}
	return h.findOrCreateUser(ctx, creds.email)
}
