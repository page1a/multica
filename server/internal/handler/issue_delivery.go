package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Issue-level canonical delivery (DENE-820): one branch/PR per issue is the
// delivery truth; every other branch is named or it blocks the merge.
//
//	GET  /api/issues/{id}/delivery            the aggregate + cleanup plan
//	PUT  /api/issues/{id}/delivery/canonical  {branch}
//	POST /api/issues/{id}/delivery/classify   {branch, role, resolution}
//	POST /api/issues/{id}/delivery/cleanup    {branch, status, note}

type issueDeliveryCanonicalRequest struct {
	Branch string `json:"branch"`
}

type issueDeliveryClassifyRequest struct {
	Branch     string `json:"branch"`
	Role       string `json:"role"`
	Resolution string `json:"resolution"`
}

type issueDeliveryCleanupRequest struct {
	Branch string `json:"branch"`
	Status string `json:"status"`
	Note   string `json:"note"`
}

func (h *Handler) GetIssueDelivery(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	h.writeIssueDelivery(w, r, issue)
}

func (h *Handler) SetIssueDeliveryCanonical(w http.ResponseWriter, r *http.Request) {
	var req issueDeliveryCanonicalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if _, err := service.SetIssueCanonicalDeliveryBranch(r.Context(), h.Queries, issue, req.Branch); err != nil {
		writeDeliveryError(w, err, "failed to set canonical delivery branch")
		return
	}
	h.writeIssueDelivery(w, r, issue)
}

func (h *Handler) ClassifyIssueDeliveryBranch(w http.ResponseWriter, r *http.Request) {
	var req issueDeliveryClassifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if _, err := service.ClassifyIssueDeliveryBranch(r.Context(), h.Queries, issue, req.Branch, req.Role, req.Resolution); err != nil {
		writeDeliveryError(w, err, "failed to classify delivery branch")
		return
	}
	h.writeIssueDelivery(w, r, issue)
}

func (h *Handler) RecordIssueDeliveryCleanup(w http.ResponseWriter, r *http.Request) {
	var req issueDeliveryCleanupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if _, err := service.RecordIssueDeliveryCleanup(r.Context(), h.Queries, issue, req.Branch, req.Status, req.Note); err != nil {
		writeDeliveryError(w, err, "failed to record delivery cleanup")
		return
	}
	h.writeIssueDelivery(w, r, issue)
}

func (h *Handler) writeIssueDelivery(w http.ResponseWriter, r *http.Request, issue db.Issue) {
	delivery, err := service.BuildIssueDelivery(r.Context(), h.Queries, issue)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load issue delivery")
		return
	}
	writeJSON(w, http.StatusOK, delivery)
}

func writeDeliveryError(w http.ResponseWriter, err error, fallback string) {
	if errors.Is(err, service.ErrDeliveryBranchInvalid) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, fallback)
}
