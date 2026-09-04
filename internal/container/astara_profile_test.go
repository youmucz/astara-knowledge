package container

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/astara"
)

func TestInvalidAndKnowledgeProfilesUseRestrictedConstruction(t *testing.T) {
	for _, profile := range []astara.Profile{
		{Name: astara.KnowledgeProfile, Valid: true},
		{Name: "unknown", Valid: false},
	} {
		if !restrictedRuntime(profile) {
			t.Fatalf("profile %+v would construct prohibited services", profile)
		}
	}
	if shouldStartProfileWorkers(astara.Profile{Name: "unknown", Valid: false}) {
		t.Fatal("unknown profile must not start worker servers")
	}
	if !shouldStartProfileWorkers(astara.Profile{Name: astara.KnowledgeProfile, Valid: true}) {
		t.Fatal("valid knowledge profile must start knowledge workers")
	}
}
