package httpapi

import (
	"encoding/json"
	"net/http"
)

type Status struct {
	Status string `json:"status"`
}

func (api *API) healthHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	status := Status{
		Status: "ok"}

	statusJson, err := json.Marshal(status)
	if err != nil {
		panic(err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(statusJson)
}
