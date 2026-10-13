package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/marketplace"
)

// MarketplaceService is the user's account on the app marketplace
// (MARKETPLACE_PLAN.md) and, for its admin, the accounts waiting for approval.
type MarketplaceService interface {
	Status(ctx context.Context) (marketplace.Status, error)
	SignUp(ctx context.Context, username, email, password string) error
	SignIn(ctx context.Context, username, password string) (marketplace.Status, error)
	SignOut(ctx context.Context) error
	PendingUsers(ctx context.Context) ([]marketplace.PendingUser, error)
	Approve(ctx context.Context, username string) error
	Decline(ctx context.Context, username string) error
	Apps(ctx context.Context) ([]marketplace.App, error)
}

func (api *API) registerMarketplaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/marketplace", api.getMarketplaceHandler)
	mux.HandleFunc("POST /api/v1/marketplace/sign-up", api.marketplaceSignUpHandler)
	mux.HandleFunc("POST /api/v1/marketplace/sign-in", api.marketplaceSignInHandler)
	mux.HandleFunc("POST /api/v1/marketplace/sign-out", api.marketplaceSignOutHandler)
	mux.HandleFunc("GET /api/v1/marketplace/pending-users", api.marketplacePendingUsersHandler)
	mux.HandleFunc("POST /api/v1/marketplace/pending-users/{username}/approve", api.marketplaceApproveHandler)
	mux.HandleFunc("POST /api/v1/marketplace/pending-users/{username}/decline", api.marketplaceDeclineHandler)
}

func (api *API) getMarketplaceHandler(w http.ResponseWriter, r *http.Request) {
	status, err := api.marketplace.Status(r.Context())
	if err != nil {
		writeMarketplaceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status, "marketplace status")
}

func (api *API) marketplaceSignUpHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	if err := api.marketplace.SignUp(r.Context(), body.Username, body.Email, body.Password); err != nil {
		writeMarketplaceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending_approval"}, "marketplace sign-up")
}

func (api *API) marketplaceSignInHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	status, err := api.marketplace.SignIn(r.Context(), body.Username, body.Password)
	if err != nil {
		writeMarketplaceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status, "marketplace status")
}

func (api *API) marketplaceSignOutHandler(w http.ResponseWriter, r *http.Request) {
	if err := api.marketplace.SignOut(r.Context()); err != nil {
		writeMarketplaceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (api *API) marketplacePendingUsersHandler(w http.ResponseWriter, r *http.Request) {
	pending, err := api.marketplace.PendingUsers(r.Context())
	if err != nil {
		writeMarketplaceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": pending}, "marketplace pending users")
}

func (api *API) marketplaceApproveHandler(w http.ResponseWriter, r *http.Request) {
	if err := api.marketplace.Approve(r.Context(), r.PathValue("username")); err != nil {
		writeMarketplaceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (api *API) marketplaceDeclineHandler(w http.ResponseWriter, r *http.Request) {
	if err := api.marketplace.Decline(r.Context(), r.PathValue("username")); err != nil {
		writeMarketplaceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeMarketplaceError(w http.ResponseWriter, err error) {
	var signup *marketplace.SignupError
	switch {
	case errors.As(err, &signup):
		writeError(w, signup.Status, "marketplace_"+signup.Code, signup.Message)
	case errors.Is(err, marketplace.ErrPendingApproval):
		writeError(w, http.StatusForbidden, "marketplace_pending_approval", "your account is waiting for approval")
	case errors.Is(err, marketplace.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, "marketplace_invalid_credentials", "the username or password is wrong")
	case errors.Is(err, marketplace.ErrNotSignedIn):
		writeError(w, http.StatusUnauthorized, "marketplace_not_signed_in", "sign in to the marketplace first")
	case errors.Is(err, marketplace.ErrNotAdmin):
		writeError(w, http.StatusForbidden, "marketplace_not_admin", "only the marketplace admin can do this")
	case errors.Is(err, marketplace.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "marketplace_user_not_found", "that account no longer exists")
	case errors.Is(err, marketplace.ErrUnavailable):
		log.Printf("marketplace: %v", err)
		writeError(w, http.StatusServiceUnavailable, "marketplace_unavailable", "the marketplace couldn't do this right now; try again later")
	default:
		log.Printf("marketplace: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
