package version

import (
	"encoding/json"
	"os"
	"testing"
)

func TestVersionMatchesManifest(t *testing.T) {
	b, err := os.ReadFile("../../extension/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct{ Version string }
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.Version != Version {
		t.Fatalf("extension manifest is %s, Go version is %s", m.Version, Version)
	}
}
