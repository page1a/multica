package handler

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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

	maxPasswordLoginUsernameLen  = 256
	maxPasswordLoginPasswordLen  = 1024
	minPasswordSignupPasswordLen = 8
	maxPasswordSignupEmailLen    = 254
)

var (
	errInvalidPasswordCreds  = errors.New("invalid username or password")
	passwordSignupUsernameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{1,31}$`)
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

type PasswordSignupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email"`
	// Totp is the shared team 2FA code. Required only when
	// MULTICA_SIGNUP_TOTP_SECRET is set; ignored when that gate is off.
	Totp string `json:"totp,omitempty"`
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

	h.writePasswordAuthSuccess(w, r, user, isNew, "user logged in via password")
}

func (h *Handler) PasswordSignup(w http.ResponseWriter, r *http.Request) {
	if _, ok := passwordAuthConfigured(); !ok {
		writeError(w, http.StatusNotFound, "password signup is not enabled")
		return
	}

	var req PasswordSignupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	username, ok := normalizeSignupUsername(req.Username)
	if !ok {
		writeError(w, http.StatusBadRequest, "username must be 2-32 characters of letters, numbers, dots, underscores, or hyphens")
		return
	}
	email, placeholderEmail, ok := resolveSignupEmail(req.Email, username)
	if !ok {
		writeError(w, http.StatusBadRequest, "a valid email address is required")
		return
	}
	password := req.Password
	if utf8.RuneCountInString(password) < minPasswordSignupPasswordLen || len(password) > maxPasswordLoginPasswordLen {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	totpSecret, totpRequired := signupTOTPSecret()
	if totpRequired {
		if strings.TrimSpace(req.Totp) == "" {
			writeError(w, http.StatusUnauthorized, "team 2FA code is required")
			return
		}
		if _, ok := verifySignupTOTP(totpSecret, req.Totp, time.Now()); !ok {
			writeError(w, http.StatusUnauthorized, "invalid or expired team 2FA code")
			return
		}
	}

	if creds, envOK := passwordAuthConfigured(); envOK && strings.EqualFold(username, creds.username) {
		writeError(w, http.StatusConflict, "username already taken")
		return
	}

	if err := h.checkSignupAllowed(r.Context(), email, true); err != nil {
		var signupErr SignupError
		if errors.As(err, &signupErr) {
			writeError(w, http.StatusForbidden, signupErr.Error())
			return
		}
		writeError(w, http.StatusForbidden, "user registration is disabled")
		return
	}

	if auth.IsTemporarilyDisabledUserEmail(email) {
		writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
		return
	}

	if _, err := h.Queries.GetUserByEmail(r.Context(), email); err == nil {
		if placeholderEmail {
			// Placeholder addresses are derived from the username, so a
			// collision is a duplicate username, not a real email conflict.
			writeError(w, http.StatusConflict, "username already taken")
			return
		}
		writeError(w, http.StatusConflict, "email already registered")
		return
	} else if !isNotFound(err) {
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	if _, err := h.Queries.GetPasswordCredentialByUsername(r.Context(), username); err == nil {
		writeError(w, http.StatusConflict, "username already taken")
		return
	} else if !isNotFound(err) {
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}
	defer tx.Rollback(r.Context())

	qtx := h.Queries.WithTx(tx)
	user, err := qtx.CreateUser(r.Context(), db.CreateUserParams{
		Name:  username,
		Email: email,
	})
	if err != nil {
		if isUniqueViolation(err) {
			if placeholderEmail {
				writeError(w, http.StatusConflict, "username already taken")
				return
			}
			writeError(w, http.StatusConflict, "email already registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	if _, err := qtx.CreatePasswordCredential(r.Context(), db.CreatePasswordCredentialParams{
		UserID:       user.ID,
		Username:     username,
		PasswordHash: string(hash),
	}); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "username already taken")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	if totpRequired {
		if err := consumeSignupTOTP(r.Context(), qtx, totpSecret, req.Totp, time.Now()); err != nil {
			if errors.Is(err, errSignupTOTPRejected) {
				writeError(w, http.StatusUnauthorized, "invalid or expired team 2FA code")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to create user")
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	h.writePasswordAuthSuccess(w, r, user, true, "user signed up via password")
}

func (h *Handler) authenticatePasswordUser(ctx context.Context, username, password string) (db.User, bool, error) {
	if creds, ok := passwordAuthConfigured(); ok && secretEqual(username, creds.username) {
		if !secretEqual(password, creds.password) {
			return db.User{}, false, errInvalidPasswordCreds
		}
		return h.findOrCreateUser(ctx, creds.email)
	}

	rec, err := h.Queries.GetPasswordCredentialByUsername(ctx, username)
	if err != nil {
		return db.User{}, false, errInvalidPasswordCreds
	}
	if bcrypt.CompareHashAndPassword([]byte(rec.PasswordHash), []byte(password)) != nil {
		return db.User{}, false, errInvalidPasswordCreds
	}

	user, err := h.Queries.GetUser(ctx, rec.UserID)
	if err != nil {
		return db.User{}, false, err
	}
	if auth.IsTemporarilyDisabledUser(uuidToString(user.ID), user.Email) {
		return db.User{}, false, auth.ErrTemporarilyDisabledUser
	}
	return user, false, nil
}

func (h *Handler) writePasswordAuthSuccess(w http.ResponseWriter, r *http.Request, user db.User, isNew bool, action string) {
	if isNew {
		obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.Signup(uuidToString(user.ID), user.Email, signupSourceFromRequest(r)))
	}

	tokenString, err := h.issueJWT(user)
	if err != nil {
		if errors.Is(err, auth.ErrTemporarilyDisabledUser) {
			writeError(w, http.StatusForbidden, auth.TemporarilyDisabledUserError)
			return
		}
		slog.Warn("password auth failed", append(logger.RequestAttrs(r), "error", err, "email", user.Email)...)
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

	slog.Info(action, append(logger.RequestAttrs(r), "user_id", uuidToString(user.ID), "email", user.Email)...)
	writeJSON(w, http.StatusOK, LoginResponse{
		Token: tokenString,
		User:  h.userToResponse(user),
	})
}

func normalizeSignupUsername(raw string) (string, bool) {
	username := strings.TrimSpace(raw)
	if !passwordSignupUsernameRe.MatchString(username) {
		return "", false
	}
	return strings.ToLower(username), true
}

func normalizeSignupEmail(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > maxPasswordSignupEmailLen {
		return "", false
	}
	addr, err := mail.ParseAddress(trimmed)
	if err != nil || !strings.EqualFold(addr.Address, trimmed) {
		return "", false
	}
	email := strings.ToLower(addr.Address)
	at := strings.LastIndex(email, "@")
	if at < 1 || at == len(email)-1 {
		return "", false
	}
	if !strings.Contains(email[at+1:], ".") {
		return "", false
	}
	return email, true
}

// RFC 2606 reserved TLD. Placeholder addresses are synthesized when signup
// email is omitted so users.email can stay NOT NULL without a migration.
const signupPlaceholderEmailDomain = "signup.invalid"

func placeholderSignupEmail(username string) string {
	return username + "@" + signupPlaceholderEmailDomain
}

// resolveSignupEmail accepts a missing/blank email by synthesizing a
// per-username placeholder. A non-empty value is still validated by
// normalizeSignupEmail (which rejects empty input and domains without a dot).
func resolveSignupEmail(raw, username string) (email string, placeholder bool, ok bool) {
	if strings.TrimSpace(raw) == "" {
		return placeholderSignupEmail(username), true, true
	}
	email, ok = normalizeSignupEmail(raw)
	if ok && strings.HasSuffix(email, "@"+signupPlaceholderEmailDomain) {
		// Reserved for synthesized placeholders; accepting it would let one
		// user squat another username's placeholder address.
		return "", false, false
	}
	return email, false, ok
}
