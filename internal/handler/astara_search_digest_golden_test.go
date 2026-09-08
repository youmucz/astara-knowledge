// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package handler

import (
	"strings"
	"testing"
)

// Expected bytes were independently generated with Python stdlib JSON,
// ensure_ascii=False and explicit U+2028/U+2029 escapes, not this helper.
func TestSearchDigestCanonicalOrderingPreservesInput(t *testing.T) {
	a := astaraSearchDocument{TenantID: "42", KnowledgeBaseID: "kb", KnowledgeID: "a", Revision: strings.Repeat("a", 64)}
	b := a
	b.KnowledgeID = "b"
	input := []astaraSearchDocument{b, a}
	want := computeSearchDigest([]astaraSearchDocument{a, b})
	if got := computeSearchDigest(input); got != want {
		t.Fatal("input order changes scope digest")
	}
	if input[0] != b || input[1] != a {
		t.Fatal("digest calculation mutated caller scope")
	}
	b.Revision = strings.Repeat("b", 64)
	if computeSearchDigest([]astaraSearchDocument{a, b}) == want {
		t.Fatal("revision drift not bound in scope digest")
	}
}

func TestSearchDigestIndependentUnicodeGolden(t *testing.T) {
	docs := []astaraSearchDocument{{TenantID: "42", KnowledgeBaseID: "kb-中文<&", KnowledgeID: "doc-\"\\\u2028\u2029\x01", Revision: strings.Repeat("a", 64)}}
	const want = "fecfaeeafa8d5d28cba6123013d0410ff050e82581302dfd14b791abd42e36c7"
	if got := computeSearchDigest(docs); got != want {
		t.Fatalf("canonical digest differs from Python: got %s want %s", got, want)
	}
}
