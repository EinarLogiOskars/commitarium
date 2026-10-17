package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/modelguide"
)

type modelGuideMemory struct {
	overrides map[string]modelguide.Override
}

func (store *modelGuideMemory) ListModelGuideOverrides(context.Context) ([]modelguide.Override, error) {
	listed := make([]modelguide.Override, 0, len(store.overrides))
	for _, override := range store.overrides {
		listed = append(listed, override)
	}
	return listed, nil
}

func (store *modelGuideMemory) SaveModelGuideOverride(_ context.Context, override modelguide.Override) error {
	store.overrides[override.ModelID] = override
	return nil
}

func (store *modelGuideMemory) DeleteModelGuideOverride(_ context.Context, id string) error {
	delete(store.overrides, id)
	return nil
}

func TestModelGuideRoutesListRateAndReset(t *testing.T) {
	guide, err := modelguide.NewService(&modelGuideMemory{overrides: map[string]modelguide.Override{}})
	if err != nil {
		t.Fatal(err)
	}
	handler := newAPI(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, guide)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	listed := call(http.MethodGet, "/api/v1/model-guide", "")
	var guideBody modelGuideResponse
	if listed.Code != http.StatusOK || json.Unmarshal(listed.Body.Bytes(), &guideBody) != nil ||
		guideBody.AsOf == "" || len(guideBody.Models) == 0 {
		t.Fatalf("list = %d %s", listed.Code, listed.Body)
	}

	rated := call(http.MethodPut, "/api/v1/model-guide/overrides/claude-opus-5", `{"tier":"frontier"}`)
	var entry modelguide.Entry
	if rated.Code != http.StatusOK || json.Unmarshal(rated.Body.Bytes(), &entry) != nil ||
		entry.Tier != modelguide.TierFrontier || entry.Source != modelguide.SourceUser {
		t.Fatalf("rate = %d %s", rated.Code, rated.Body)
	}
	if bad := call(http.MethodPut, "/api/v1/model-guide/overrides/claude-opus-5", `{"tier":"great"}`); bad.Code != http.StatusBadRequest {
		t.Fatalf("bad tier = %d %s", bad.Code, bad.Body)
	}

	reset := call(http.MethodDelete, "/api/v1/model-guide/overrides/claude-opus-5", "")
	if reset.Code != http.StatusOK || json.Unmarshal(reset.Body.Bytes(), &entry) != nil || entry.Source != modelguide.SourceOfficial {
		t.Fatalf("reset = %d %s", reset.Code, reset.Body)
	}
}
