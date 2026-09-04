package container

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// disabledAgentShareService keeps the knowledge-only DI graph closed: agents
// (and therefore agent shares) do not exist under the profile, while knowledge
// base handlers still reference the share service for "visible via shared
// agent" lookups. Those lookups must answer "not visible" without ever
// consulting a disabled module.
type disabledAgentShareService struct{}

func newDisabledAgentShareService() interfaces.AgentShareService {
	return &disabledAgentShareService{}
}

func (*disabledAgentShareService) ShareAgent(context.Context, string, string, string, uint64, types.OrgMemberRole) (*types.AgentShare, error) {
	return nil, errAgentsDisabled
}

func (*disabledAgentShareService) RemoveShare(context.Context, string, string, uint64) error {
	return errAgentsDisabled
}

func (*disabledAgentShareService) ListSharesByAgent(context.Context, string, uint64) ([]*types.AgentShare, error) {
	return nil, errAgentsDisabled
}

func (*disabledAgentShareService) ListSharesByOrganization(context.Context, string) ([]*types.AgentShare, error) {
	return nil, errAgentsDisabled
}

func (*disabledAgentShareService) ListSharedAgents(context.Context, uint64, types.TenantRole) ([]*types.SharedAgentInfo, error) {
	return []*types.SharedAgentInfo{}, nil
}

func (*disabledAgentShareService) ListSharedAgentsInOrganization(context.Context, string, uint64, types.TenantRole) ([]*types.OrganizationSharedAgentItem, error) {
	return []*types.OrganizationSharedAgentItem{}, nil
}

func (*disabledAgentShareService) ListSharedAgentsInOrganizations(context.Context, []string, uint64, types.TenantRole) (map[string][]*types.OrganizationSharedAgentItem, error) {
	return map[string][]*types.OrganizationSharedAgentItem{}, nil
}

func (*disabledAgentShareService) GetSharedAgentForTenant(context.Context, uint64, types.TenantRole, string, ...uint64) (*types.CustomAgent, error) {
	return nil, errAgentsDisabled
}

// No shared agents exist under the profile, so no knowledge base is visible
// through one.
func (*disabledAgentShareService) TenantCanAccessKBViaSomeSharedAgent(context.Context, uint64, types.TenantRole, *types.KnowledgeBase) (bool, error) {
	return false, nil
}

func (*disabledAgentShareService) SetSharedAgentDisabledByMe(context.Context, uint64, string, uint64, bool) error {
	return errAgentsDisabled
}

func (*disabledAgentShareService) GetShare(context.Context, string) (*types.AgentShare, error) {
	return nil, errAgentsDisabled
}

func (*disabledAgentShareService) GetShareByAgentAndOrg(context.Context, string, string) (*types.AgentShare, error) {
	return nil, errAgentsDisabled
}

func (*disabledAgentShareService) GetShareByAgentIDForTenant(context.Context, uint64, string, uint64) (*types.AgentShare, error) {
	return nil, errAgentsDisabled
}

func (*disabledAgentShareService) CountByOrganizations(context.Context, []string) (map[string]int64, error) {
	return map[string]int64{}, nil
}
