package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/testaccounts"
)

// TestAccountService keeps each project's test logins for its preview
// (HELPERS_PLAN.md, H3). Only Password and Preview answer passwords.
type TestAccountService interface {
	List(ctx context.Context, projectID string) ([]testaccounts.Summary, error)
	Add(ctx context.Context, projectID, label, role, note string) (testaccounts.Summary, error)
	Update(ctx context.Context, projectID, username string, role, note *string) (testaccounts.Summary, error)
	Remove(ctx context.Context, projectID, username string) error
	Password(ctx context.Context, projectID, username string) (string, error)
	Preview(ctx context.Context, projectID string) (testaccounts.PreviewFile, error)
	DeleteProject(ctx context.Context, projectID string) error
}

type testAccountsResponse struct {
	Accounts []testaccounts.Summary `json:"accounts"`
}

type addTestAccountRequest struct {
	Label string `json:"label"`
	Role  string `json:"role"`
	Note  string `json:"note"`
}

type updateTestAccountRequest struct {
	Role *string `json:"role"`
	Note *string `json:"note"`
}

// Settings lists, adds, changes and removes the logins, and copies one
// password; the host reads the preview's file when it starts a preview.
func (api *API) registerTestAccountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/projects/{id}/test-accounts", api.listTestAccountsHandler)
	mux.HandleFunc("POST /api/v1/projects/{id}/test-accounts", api.addTestAccountHandler)
	mux.HandleFunc("GET /api/v1/projects/{id}/test-accounts/preview", api.previewTestAccountsHandler)
	mux.HandleFunc("PATCH /api/v1/projects/{id}/test-accounts/{username}", api.updateTestAccountHandler)
	mux.HandleFunc("DELETE /api/v1/projects/{id}/test-accounts/{username}", api.removeTestAccountHandler)
	mux.HandleFunc("GET /api/v1/projects/{id}/test-accounts/{username}/password", api.testAccountPasswordHandler)
}

func (api *API) listTestAccountsHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !api.testAccountProject(w, r, projectID) {
		return
	}
	accounts, err := api.testAccounts.List(r.Context(), projectID)
	if err != nil {
		writeTestAccountError(w, projectID, err)
		return
	}
	writeJSON(w, http.StatusOK, testAccountsResponse{Accounts: accounts}, "test accounts")
}

func (api *API) addTestAccountHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	var body addTestAccountRequest
	if !decodeSingleJSON(w, r, &body) || !api.testAccountProject(w, r, projectID) {
		return
	}
	added, err := api.testAccounts.Add(r.Context(), projectID, body.Label, body.Role, body.Note)
	if err != nil {
		writeTestAccountError(w, projectID, err)
		return
	}
	writeJSON(w, http.StatusCreated, added, "test account")
}

func (api *API) updateTestAccountHandler(w http.ResponseWriter, r *http.Request) {
	projectID, username := r.PathValue("id"), r.PathValue("username")
	var body updateTestAccountRequest
	if !decodeSingleJSON(w, r, &body) || !api.testAccountProject(w, r, projectID) {
		return
	}
	updated, err := api.testAccounts.Update(r.Context(), projectID, username, body.Role, body.Note)
	if err != nil {
		writeTestAccountError(w, projectID, err)
		return
	}
	writeJSON(w, http.StatusOK, updated, "test account")
}

func (api *API) removeTestAccountHandler(w http.ResponseWriter, r *http.Request) {
	projectID, username := r.PathValue("id"), r.PathValue("username")
	if !api.testAccountProject(w, r, projectID) {
		return
	}
	if err := api.testAccounts.Remove(r.Context(), projectID, username); err != nil {
		writeTestAccountError(w, projectID, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testAccountPasswordHandler answers one password, for the user to copy;
// nothing keeps it.
func (api *API) testAccountPasswordHandler(w http.ResponseWriter, r *http.Request) {
	projectID, username := r.PathValue("id"), r.PathValue("username")
	if !api.testAccountProject(w, r, projectID) {
		return
	}
	password, err := api.testAccounts.Password(r.Context(), projectID, username)
	if err != nil {
		writeTestAccountError(w, projectID, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"password": password}, "test account password")
}

// previewTestAccountsHandler answers the file the host gives the preview's
// containers at testaccounts.PreviewPath.
func (api *API) previewTestAccountsHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !api.testAccountProject(w, r, projectID) {
		return
	}
	file, err := api.testAccounts.Preview(r.Context(), projectID)
	if err != nil {
		writeTestAccountError(w, projectID, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, file, "preview test accounts")
}

func (api *API) testAccountProject(w http.ResponseWriter, r *http.Request, projectID string) bool {
	if _, err := api.projects.GetByID(r.Context(), projectID); err != nil {
		writeTestAccountError(w, projectID, err)
		return false
	}
	return true
}

// writeTestAccountError never puts a login's details in the log or the
// answer.
func writeTestAccountError(w http.ResponseWriter, projectID string, err error) {
	switch {
	case errors.Is(err, project.ErrNotFound):
		writeError(w, http.StatusNotFound, "project_not_found", "project not found")
	case errors.Is(err, testaccounts.ErrNotFound):
		writeError(w, http.StatusNotFound, "test_account_not_found", "no such test account")
	case errors.Is(err, testaccounts.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_test_account",
			"give the test account a name of up to 60 characters, a role of up to 40 and a note of up to 300")
	case errors.Is(err, testaccounts.ErrTooMany):
		writeError(w, http.StatusConflict, "too_many_test_accounts", "a project keeps at most 50 test accounts")
	default:
		log.Printf("test accounts of project %q are unavailable", projectID)
		writeError(w, http.StatusServiceUnavailable, "test_accounts_unavailable", "the test accounts could not be read")
	}
}
