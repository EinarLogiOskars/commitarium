package toolchain

import "testing"

func TestJavaPresetsUseExplicitWorkerAvailableToolchains(t *testing.T) {
	want := map[string]map[string]string{
		"java-gradle": {
			"java": "temurin-21.0.12+8.0.LTS", "gradle": "9.7.1",
		},
		"java-maven": {
			"java": "temurin-21.0.12+8.0.LTS", "maven": "3.9.16",
		},
		"java25-gradle": {
			"java": "temurin-25.0.4+7.0.LTS", "gradle": "9.7.1",
		},
		"java25-maven": {
			"java": "temurin-25.0.4+7.0.LTS", "maven": "3.9.16",
		},
	}
	for _, preset := range Presets() {
		expected, ok := want[preset.ID]
		if !ok {
			continue
		}
		delete(want, preset.ID)
		manifest, err := NormalizeManifest(Manifest{Source: SourcePicker, Tools: preset.Tools})
		if err != nil {
			t.Fatalf("normalize preset %q: %v", preset.ID, err)
		}
		for tool, version := range expected {
			if manifest.Tools[tool] != version {
				t.Fatalf("preset %q %s=%q want %q", preset.ID, tool, manifest.Tools[tool], version)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing Java presets: %v", want)
	}
}
