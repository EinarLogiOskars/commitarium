package marketplace

import (
	"encoding/json"
	"errors"
	"strings"
)

// AppManifestPath is where an app's name and description travel with its
// repository, beside its stack and vision (ADR-026).
const AppManifestPath = ".commitarium/app.json"

// AppManifest is how an app presents itself on the marketplace.
type AppManifest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

const (
	maxAppName        = 80
	maxAppDescription = 4000
)

// EncodeAppManifest writes an app manifest for AppManifestPath.
func EncodeAppManifest(manifest AppManifest) ([]byte, error) {
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.Description = strings.TrimSpace(manifest.Description)
	if manifest.Name == "" || len(manifest.Name) > maxAppName || len(manifest.Description) > maxAppDescription {
		return nil, errors.New("an app needs a name of at most 80 bytes and a description of at most 4000")
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// ParseAppManifest reads an app manifest; unknown fields are ignored.
func ParseAppManifest(contents []byte) (AppManifest, error) {
	var manifest AppManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		return AppManifest{}, err
	}
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.Description = strings.TrimSpace(manifest.Description)
	if manifest.Name == "" || len(manifest.Name) > maxAppName || len(manifest.Description) > maxAppDescription {
		return AppManifest{}, errors.New("the app manifest needs a short name")
	}
	return manifest, nil
}
