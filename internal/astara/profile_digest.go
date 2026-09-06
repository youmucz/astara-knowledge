// Package astara defines the canonical feature-profile digest shared with
// Plane release admission. The canonicalization mirrors
// scripts/profile_digest.py byte-for-byte: compact JSON with sorted keys and
// sorted feature lists over the closed knowledge profile.
package astara

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// profileDocument field order mirrors scripts/profile_digest.py exactly:
// Python canonicalizes with sort_keys=True (enabled < name < prohibited), and
// encoding/json emits fields in declaration order.
type profileDocument struct {
	Enabled    []string `json:"enabled"`
	Name       string   `json:"name"`
	Prohibited []string `json:"prohibited"`
}

// ProfileDigest returns the sha256 of the canonical profile document.
// Plane admission compares this value against the pinned dependency manifest;
// any drift means the closed feature boundary changed and must fail closed.
func ProfileDigest(profile Profile) string {
	enabled := make([]string, 0, len(knowledgeFeatures))
	for feature := range knowledgeFeatures {
		enabled = append(enabled, string(feature))
	}
	sort.Strings(enabled)
	prohibited := make([]string, 0, len(prohibitedFeatures))
	for _, feature := range prohibitedFeatures {
		prohibited = append(prohibited, string(feature))
	}
	sort.Strings(prohibited)

	document := profileDocument{Name: profile.Name, Enabled: enabled, Prohibited: prohibited}
	payload, err := json.Marshal(document)
	if err != nil {
		// json.Marshal cannot fail for this closed struct.
		return ""
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}
