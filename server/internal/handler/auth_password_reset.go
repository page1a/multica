package handler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"golang.org/x/crypto/bcrypt"
)

// Password reset (DENE-1416). Two entry points share one write path:
//
//   - POST /auth/reset-password: the forgot-password flow. Username + the
//     team 2FA code (MULTICA_SIGNUP_TOTP_SECRET, the same one signup uses) +
//     a new password. No email involved — most self-host accounts carry a
//     placeholder address.
//   - POST /api/workspaces/{id}/members/{memberId}/reset-password: a workspace
//     owner or admin issues a temporary password that is returned exactly once.
//
// Existing sessions are left alone on purpose. UI JWTs are stateless (see
// auth/session.go) so there is nothing to revoke without adding a DB read to
// every request, and revoking PATs would silently stop the member's daemons.
// Only accounts in user_password_credential can be reset; the env bootstrap
// account (MULTICA_PASSWORD_AUTH_*) lives in configuration, not here.

const (
	passwordResetMethodSelf  = "self"
	passwordResetMethodAdmin = "admin"

	// Ambiguous glyphs (0/O, 1/l/I) are left out so a temporary password can
	// be read aloud or copied by hand.
	tempPasswordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	tempPasswordLen      = 16
)

// passwordResetRejectedMessage is the one answer the forgot-password flow gives
// for an unknown username, a wrong code, or a reused code, so a caller cannot
// tell which part was wrong.
const passwordResetRejectedMessage = "invalid username or team 2FA code"

type PasswordResetRequest struct {
	Username    string `json:"username"`
	Totp        string `json:"totp"`
	NewPassword string `json:"new_password"`
}

type MemberPasswordResetResponse struct {
	UserID            string `json:"user_id"`
	Username          string `json:"username"`
	TemporaryPassword string `json:"temporary_password"`
}

func validNewPassword(password string) bool {
	return utf8.RuneCountInString(password) >= minPasswordSignupPasswordLen && len(password) <= maxPasswordLoginPasswordLen
}

func (h *Handler) PasswordReset(w http.ResponseWriter, r *http.Request) {
	creds, ok := passwordAuthConfigured()
	if !ok {
		writeError(w, http.StatusNotFound, "password reset is not enabled")
		return
	}
	totpSecret, totpRequired := signupTOTPSecret()
	if !totpRequired {
		// Without the team 2FA gate a username alone would be enough to take
		// over an account.
		writeError(w, http.StatusNotFound, "password reset is not enabled")
		return
	}

	var req PasswordResetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" || len(username) > maxPasswordLoginUsernameLen || strings.TrimSpace(req.Totp) == "" {
		writeError(w, http.StatusBadRequest, "username, team 2FA code and new password are required")
		return
	}
	if !validNewPassword(req.NewPassword) {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	now := time.Now()
	if _, ok := verifySignupTOTP(totpSecret, req.Totp, now); !ok {
		writeError(w, http.StatusUnauthorized, passwordResetRejectedMessage)
		return
	}
	if secretEqual(username, creds.username) {
		writeError(w, http.StatusUnauthorized, passwordResetRejectedMessage)
		return
	}
	rec, err := h.Queries.GetPasswordCredentialByUsername(r.Context(), username)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusUnauthorized, passwordResetRejectedMessage)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to reset password")
		return
	}

	err = h.applyPasswordReset(r.Context(), rec.UserID, req.NewPassword, func(qtx *db.Queries) error {
		return consumeSignupTOTP(r.Context(), qtx, totpSecret, req.Totp, now)
	}, db.InsertPasswordResetAuditParams{
		UserID:   rec.UserID,
		Method:   passwordResetMethodSelf,
		ClientIp: h.clientIPForRateLimit(r),
	})
	if err != nil {
		if errors.Is(err, errSignupTOTPRejected) {
			writeError(w, http.StatusUnauthorized, passwordResetRejectedMessage)
			return
		}
		slog.Warn("password reset failed", append(logger.RequestAttrs(r), "error", err, "user_id", uuidToString(rec.UserID))...)
		writeError(w, http.StatusInternalServerError, "failed to reset password")
		return
	}

	slog.Info("password reset via team 2FA", append(logger.RequestAttrs(r), "user_id", uuidToString(rec.UserID))...)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ResetMemberPassword(w http.ResponseWriter, r *http.Request) {
	if isMachineCredentialActor(r) {
		writeError(w, http.StatusForbidden, "this endpoint is only available to human actors")
		return
	}
	if _, ok := passwordAuthConfigured(); !ok {
		writeError(w, http.StatusNotFound, "password login is not enabled")
		return
	}
	workspaceID := workspaceIDFromURL(r, "id")
	requester, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	if !roleAllowed(requester.Role, "owner", "admin") {
		writeError(w, http.StatusForbidden, "only a workspace owner or admin can reset passwords")
		return
	}

	memberUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "memberId"), "member id")
	if !ok {
		return
	}
	target, err := h.Queries.GetMember(r.Context(), memberUUID)
	if err != nil || uuidToString(target.WorkspaceID) != uuidToString(requester.WorkspaceID) {
		writeError(w, http.StatusNotFound, "member not found")
		return
	}
	if uuidToString(target.UserID) == uuidToString(requester.UserID) {
		writeError(w, http.StatusBadRequest, "use forgot password on the sign-in page to reset your own password")
		return
	}

	// Taking over an account hands over every workspace it belongs to. Refuse
	// when the member is owner or admin somewhere the requester does not own.
	// That includes this workspace when the requester is an admin, so an admin
	// can reset members and guests but not another admin or an owner.
	privileged, err := h.Queries.CountPrivilegedMembershipsOutsideOwner(r.Context(), db.CountPrivilegedMembershipsOutsideOwnerParams{
		TargetUserID: target.UserID,
		ActorUserID:  requester.UserID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reset password")
		return
	}
	if privileged > 0 {
		writeError(w, http.StatusForbidden, "this member is an owner or admin, and only the owner of that workspace can reset their password")
		return
	}

	rec, err := h.Queries.GetPasswordCredentialByUserID(r.Context(), target.UserID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusBadRequest, "this member does not sign in with a password")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to reset password")
		return
	}

	temp, err := generateTempPassword()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reset password")
		return
	}
	err = h.applyPasswordReset(r.Context(), rec.UserID, temp, nil, db.InsertPasswordResetAuditParams{
		UserID:      rec.UserID,
		Method:      passwordResetMethodAdmin,
		ActorUserID: requester.UserID,
		WorkspaceID: requester.WorkspaceID,
		ClientIp:    h.clientIPForRateLimit(r),
	})
	if err != nil {
		slog.Warn("member password reset failed", append(logger.RequestAttrs(r), "error", err, "user_id", uuidToString(rec.UserID), "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to reset password")
		return
	}

	slog.Info("member password reset by workspace manager", append(logger.RequestAttrs(r), "user_id", uuidToString(rec.UserID), "workspace_id", workspaceID)...)
	writeJSON(w, http.StatusOK, MemberPasswordResetResponse{
		UserID:            uuidToString(rec.UserID),
		Username:          rec.Username,
		TemporaryPassword: temp,
	})
}

// applyPasswordReset writes the new hash and the audit row in one
// transaction. guard runs inside the same transaction so a consumed 2FA step
// and the password change land or roll back together.
func (h *Handler) applyPasswordReset(ctx context.Context, userID pgtype.UUID, password string, guard func(*db.Queries) error, audit db.InsertPasswordResetAuditParams) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	qtx := h.Queries.WithTx(tx)
	if guard != nil {
		if err := guard(qtx); err != nil {
			return err
		}
	}
	if _, err := qtx.UpdatePasswordCredentialHash(ctx, db.UpdatePasswordCredentialHashParams{
		UserID:       userID,
		PasswordHash: string(hash),
	}); err != nil {
		return err
	}
	if err := qtx.InsertPasswordResetAudit(ctx, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func generateTempPassword() (string, error) {
	max := big.NewInt(int64(len(tempPasswordAlphabet)))
	var b strings.Builder
	b.Grow(tempPasswordLen)
	for i := 0; i < tempPasswordLen; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(tempPasswordAlphabet[n.Int64()])
	}
	return b.String(), nil
}
