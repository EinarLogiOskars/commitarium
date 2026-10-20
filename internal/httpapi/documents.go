package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/documents"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// DocumentService keeps the files attached to a project (planning mode):
// specs, API docs, sample exports that the assistant, its helpers, and the
// work orders' agents read in their checkouts.
type DocumentService interface {
	List(ctx context.Context, projectID string) ([]documents.Document, error)
	Add(ctx context.Context, projectID, name string, contents []byte) (documents.Document, error)
	Delete(ctx context.Context, projectID, name string) error
}

func (api *API) registerDocumentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/projects/{id}/documents", api.listDocumentsHandler)
	mux.HandleFunc("POST /api/v1/projects/{id}/documents", api.addDocumentHandler)
	mux.HandleFunc("DELETE /api/v1/projects/{id}/documents/{name}", api.deleteDocumentHandler)
}

func (api *API) listDocumentsHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !api.documentProject(w, r, projectID) {
		return
	}
	listed, err := api.documents.List(r.Context(), projectID)
	if err != nil {
		writeDocumentError(w, projectID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]documents.Document{"documents": listed}, "documents")
}

type addDocumentRequest struct {
	Name          string `json:"name"`
	ContentBase64 string `json:"content_base64"`
}

func (api *API) addDocumentHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	var body addDocumentRequest
	// A document's base64 is a third larger than the document.
	decoder := json.NewDecoder(io.LimitReader(r.Body, documents.MaxBytes*4/3+4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "the document must be JSON with its name and content_base64, at most 5 MB")
		return
	}
	contents, err := base64.StdEncoding.DecodeString(body.ContentBase64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_document", "the document's content must be base64")
		return
	}
	if !api.documentProject(w, r, projectID) {
		return
	}
	added, err := api.documents.Add(r.Context(), projectID, body.Name, contents)
	if err != nil {
		writeDocumentError(w, projectID, err)
		return
	}
	writeJSON(w, http.StatusCreated, added, "document")
}

func (api *API) deleteDocumentHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !api.documentProject(w, r, projectID) {
		return
	}
	if err := api.documents.Delete(r.Context(), projectID, r.PathValue("name")); err != nil {
		writeDocumentError(w, projectID, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// documentProject checks that the project exists.
func (api *API) documentProject(w http.ResponseWriter, r *http.Request, projectID string) bool {
	if _, err := api.projects.GetByID(r.Context(), projectID); err != nil {
		writeDocumentError(w, projectID, err)
		return false
	}
	return true
}

func writeDocumentError(w http.ResponseWriter, projectID string, err error) {
	switch {
	case errors.Is(err, project.ErrNotFound):
		writeError(w, http.StatusNotFound, "project_not_found", "project not found")
	case errors.Is(err, documents.ErrNotFound):
		writeError(w, http.StatusNotFound, "document_not_found", err.Error())
	case errors.Is(err, documents.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_document", err.Error())
	case errors.Is(err, documents.ErrTooMany):
		writeError(w, http.StatusConflict, "too_many_documents", err.Error())
	default:
		log.Printf("documents of project %q: %v", projectID, err)
		writeError(w, http.StatusServiceUnavailable, "documents_unavailable", "the project's documents are unavailable")
	}
}
