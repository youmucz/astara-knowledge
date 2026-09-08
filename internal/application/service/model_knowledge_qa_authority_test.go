// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

// The provider is not a KnowledgeQA model-configuration authority: the only
// KnowledgeQA mutation the model service accepts is the reserved
// plane-owned row maintained by the closed push contract.
func TestEnsureKnowledgeQAAuthority(t *testing.T) {
	embedding := &types.Model{ID: "emb-1", Type: types.ModelTypeEmbedding}
	if err := ensureKnowledgeQAAuthority(embedding); err != nil {
		t.Fatalf("embedding model rejected: %v", err)
	}
	plane := &types.Model{
		ID:        types.PlaneOwnedKnowledgeQAModelID,
		Type:      types.ModelTypeKnowledgeQA,
		ManagedBy: types.PlaneManagedBy,
	}
	if err := ensureKnowledgeQAAuthority(plane); err != nil {
		t.Fatalf("plane-owned row rejected: %v", err)
	}
	rogue := &types.Model{ID: "qa-1", Type: types.ModelTypeKnowledgeQA, ManagedBy: ""}
	if err := ensureKnowledgeQAAuthority(rogue); err == nil {
		t.Fatal("provider-originated KnowledgeQA row was accepted")
	}
	rogueByID := &types.Model{
		ID:        types.PlaneOwnedKnowledgeQAModelID,
		Type:      types.ModelTypeKnowledgeQA,
		ManagedBy: "ui",
	}
	if err := ensureKnowledgeQAAuthority(rogueByID); err == nil {
		t.Fatal("reserved ID with a foreign managed-by origin was accepted")
	}
}
