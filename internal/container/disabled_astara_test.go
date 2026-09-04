package container

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

// The knowledge-only profile substitutes disabled implementations for agent,
// agent-share, and memory dependencies that knowledge handlers still
// reference. Their semantics must stay closed: usage counts are zero,
// visibility is false, memory is unavailable, and mutations fail.

func TestDisabledCustomAgentRepositoryReportsZeroUsage(t *testing.T) {
	repo := newDisabledCustomAgentRepository()
	ctx := context.Background()

	count, err := repo.CountByModelID(ctx, 1, "model-id")
	if err != nil || count != 0 {
		t.Fatalf("CountByModelID = (%d, %v), want (0, nil)", count, err)
	}
	usages, err := repo.ListModelUsages(ctx, 1, "model-id")
	if err != nil || len(usages) != 0 {
		t.Fatalf("ListModelUsages = (%d, %v), want (0, nil)", len(usages), err)
	}
	if err := repo.CreateAgent(ctx, &types.CustomAgent{}); err == nil {
		t.Fatal("CreateAgent must fail under the knowledge-only profile")
	}
}

func TestDisabledAgentShareServiceAnswersNotVisible(t *testing.T) {
	shares := newDisabledAgentShareService()
	ctx := context.Background()

	if can, err := shares.TenantCanAccessKBViaSomeSharedAgent(ctx, 1, types.TenantRoleViewer, &types.KnowledgeBase{}); can || err != nil {
		t.Fatalf("TenantCanAccessKBViaSomeSharedAgent = (%v, %v), want (false, nil)", can, err)
	}
	agents, err := shares.ListSharedAgents(ctx, 1, types.TenantRoleViewer)
	if err != nil || len(agents) != 0 {
		t.Fatalf("ListSharedAgents = (%d, %v), want (0, nil)", len(agents), err)
	}
	if _, err := shares.GetSharedAgentForTenant(ctx, 1, types.TenantRoleViewer, "agent-id"); err == nil {
		t.Fatal("GetSharedAgentForTenant must fail under the knowledge-only profile")
	}
}

func TestDisabledMemoryServiceIsUnavailable(t *testing.T) {
	memory := newDisabledMemoryService()
	ctx := context.Background()

	if memory.MemoryAvailable(ctx) {
		t.Fatal("MemoryAvailable must be false under the knowledge-only profile")
	}
	if ids := memory.FamiliarKnowledgeIDs(ctx); ids != nil {
		t.Fatalf("FamiliarKnowledgeIDs = %v, want nil", ids)
	}
	if err := memory.SetEnabled(ctx, true); err == nil {
		t.Fatal("SetEnabled must fail under the knowledge-only profile")
	}
}
