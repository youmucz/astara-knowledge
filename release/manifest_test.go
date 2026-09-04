package release_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/Tencent/WeKnora/internal/astara"
)

type manifest struct {
	SchemaVersion         int               `json:"schema_version"`
	ImplementationVersion string            `json:"implementation_version"`
	UpstreamBaseline      string            `json:"upstream_baseline"`
	UpstreamCommit        string            `json:"upstream_commit"`
	FeatureProfile        string            `json:"feature_profile"`
	Contracts             map[string]int    `json:"contracts"`
	Images                map[string]string `json:"images"`
}

func TestReleaseManifestMatchesRuntimeContract(t *testing.T) {
	raw, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var got manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 1 || got.ImplementationVersion != astara.ImplementationVersion ||
		got.UpstreamBaseline != astara.UpstreamBaseline || got.UpstreamCommit != astara.UpstreamCommit ||
		got.FeatureProfile != astara.KnowledgeProfile {
		t.Fatalf("manifest identity drift: %+v", got)
	}
	want := map[string]int{"api": 1, "ui": 1, "source": 1, "tool": 1, "readiness": 1, "migration": 1}
	for key, version := range want {
		if got.Contracts[key] != version {
			t.Fatalf("contract %s=%d, want %d", key, got.Contracts[key], version)
		}
	}
	for _, image := range []string{"api", "web", "docreader"} {
		if got.Images[image] == "" {
			t.Fatalf("missing image identity %s", image)
		}
	}
	if len(got.Contracts) != len(want) || len(got.Images) != 3 {
		t.Fatalf("unknown manifest field: contracts=%v images=%v", got.Contracts, got.Images)
	}
}
