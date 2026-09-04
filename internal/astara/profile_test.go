package astara

import "testing"

func TestKnowledgeProfileIsClosed(t *testing.T) {
	t.Setenv(ProfileEnv, KnowledgeProfile)
	p := CurrentProfile()
	if !p.Valid || !p.Enabled(FeatureKnowledge) {
		t.Fatalf("knowledge profile invalid: %+v", p)
	}
	for _, feature := range ProhibitedFeatures() {
		if p.Enabled(feature) {
			t.Fatalf("prohibited feature %q is enabled", feature)
		}
	}
}

func TestUnknownProfileFailsClosed(t *testing.T) {
	t.Setenv(ProfileEnv, "future-profile")
	p := CurrentProfile()
	if p.Valid || len(p.Features()) != 0 || p.Enabled(FeatureKnowledge) {
		t.Fatalf("unknown profile must fail closed: %+v", p)
	}
}
