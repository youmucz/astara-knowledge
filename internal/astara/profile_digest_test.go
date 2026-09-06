package astara

import "testing"

// Expected value is computed by scripts/profile_digest.py from the closed
// knowledge profile; both sides must agree byte-for-byte or Plane admission
// fails closed.
const expectedProfileDigest = "sha256:0529ddfd1d057a32c5978c5cc027bf2d6c67c6fa24eba0c4f9df0166ecd12779"

func TestProfileDigestIsStableAndCanonical(t *testing.T) {
	profile := Profile{Name: KnowledgeProfile, Valid: true}
	if got := ProfileDigest(profile); got != expectedProfileDigest {
		t.Fatalf("profile digest drift: got %s want %s", got, expectedProfileDigest)
	}
	// An invalid profile must never inherit the closed feature set.
	invalid := Profile{Name: "unknown", Valid: false}
	digest := ProfileDigest(invalid)
	if digest == expectedProfileDigest {
		t.Fatalf("invalid profile reused the knowledge digest: %s", digest)
	}
}
