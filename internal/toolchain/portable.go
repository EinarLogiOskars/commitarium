package toolchain

import (
	"encoding/json"
	"fmt"
)

// PortableStackPath is where a project's stack travels with its repository,
// so a copy imported elsewhere starts configured.
const PortableStackPath = ".commitarium/stack.json"

// portableStack is the stack without machine state such as provisioning.
type portableStack struct {
	Tools    map[string]string `json:"tools"`
	Services []string          `json:"services"`
	Run      *Run              `json:"run,omitempty"`
}

// PortableStack encodes a configured stack for PortableStackPath.
func PortableStack(manifest Manifest) ([]byte, error) {
	manifest.Source = SourceDetected
	normalized, err := NormalizeManifest(manifest)
	if err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(portableStack{
		Tools: normalized.Tools, Services: normalized.Services, Run: normalized.Run,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// ParsePortableStack reads a stack written by PortableStack. Unknown fields
// are ignored so newer files stay readable.
func ParsePortableStack(contents []byte) (Manifest, error) {
	var stored portableStack
	if err := json.Unmarshal(contents, &stored); err != nil {
		return Manifest{}, fmt.Errorf("%w: %s is not valid JSON", ErrInvalidManifest, PortableStackPath)
	}
	return NormalizeManifest(Manifest{
		Source: SourceDetected, Tools: stored.Tools, Services: stored.Services, Run: stored.Run,
	})
}
