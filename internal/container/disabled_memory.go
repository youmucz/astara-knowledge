package container

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"

	"github.com/hibiken/asynq"
)

var errMemoryDisabled = errors.New("memory is disabled by the active feature profile")

// disabledMemoryService closes the knowledge-only DI graph where wiki handlers
// accept a memory service for familiar-knowledge ranking. Under the profile,
// memory does not exist: availability reports false, familiar rankings stay
// empty, and every mutation fails closed.
type disabledMemoryService struct{}

func newDisabledMemoryService() interfaces.MemoryService {
	return &disabledMemoryService{}
}

func (*disabledMemoryService) Recall(context.Context, string) interfaces.MemoryRecall {
	return interfaces.MemoryRecall{}
}

func (*disabledMemoryService) SearchMemory(context.Context, string, int) interfaces.MemorySearchResult {
	return interfaces.MemorySearchResult{}
}

func (*disabledMemoryService) MemoryAvailable(context.Context) bool {
	return false
}

func (*disabledMemoryService) RetrievalContextFor(context.Context) interfaces.RetrievalContext {
	return interfaces.RetrievalContext{}
}

func (*disabledMemoryService) DocumentAffinity(context.Context, []string) map[string]int {
	return map[string]int{}
}

func (*disabledMemoryService) RecordAnswerSources(context.Context, []types.MemoryDocAffinity) {}

func (*disabledMemoryService) ObserveQuestionTopics(context.Context, []string) []string {
	return nil
}

func (*disabledMemoryService) Remember(context.Context, types.MemoryItem) (*types.MemoryItem, error) {
	return nil, errMemoryDisabled
}

func (*disabledMemoryService) ScheduleExtraction(context.Context, string, string, string) {}

func (*disabledMemoryService) Handle(context.Context, *asynq.Task) error {
	return errMemoryDisabled
}

func (*disabledMemoryService) ListItems(context.Context, string, int, int) ([]*types.MemoryItem, int64, error) {
	return []*types.MemoryItem{}, 0, nil
}

func (*disabledMemoryService) ListTopics(context.Context, int, int) ([]*types.MemoryTopicView, int64, error) {
	return []*types.MemoryTopicView{}, 0, nil
}

func (*disabledMemoryService) PromoteTopic(context.Context, string) (*types.MemoryItem, error) {
	return nil, errMemoryDisabled
}

func (*disabledMemoryService) DeleteTopic(context.Context, string) error {
	return errMemoryDisabled
}

func (*disabledMemoryService) ListDocuments(context.Context, int, int) ([]*types.MemoryDocView, int64, error) {
	return []*types.MemoryDocView{}, 0, nil
}

func (*disabledMemoryService) DeleteDocument(context.Context, string) error {
	return errMemoryDisabled
}

// Wiki familiar-knowledge ranking: no memory, no familiar knowledge.
func (*disabledMemoryService) FamiliarKnowledgeIDs(context.Context) []string {
	return nil
}

func (*disabledMemoryService) CreateItem(context.Context, string, string, int) (*types.MemoryItem, error) {
	return nil, errMemoryDisabled
}

func (*disabledMemoryService) ConfirmItem(context.Context, string) (*types.MemoryItem, error) {
	return nil, errMemoryDisabled
}

func (*disabledMemoryService) RejectItem(context.Context, string) error {
	return errMemoryDisabled
}

func (*disabledMemoryService) UpdateItem(context.Context, string, string, int) (*types.MemoryItem, error) {
	return nil, errMemoryDisabled
}

func (*disabledMemoryService) DeleteItem(context.Context, string) error {
	return errMemoryDisabled
}

func (*disabledMemoryService) Clear(context.Context) (int64, error) {
	return 0, errMemoryDisabled
}

func (*disabledMemoryService) ConsolidateNow(context.Context) (*types.MemoryConsolidationResult, error) {
	return nil, errMemoryDisabled
}

func (*disabledMemoryService) GetSettings(context.Context) (*types.MemorySettings, error) {
	return nil, errMemoryDisabled
}

func (*disabledMemoryService) SetEnabled(context.Context, bool) error {
	return errMemoryDisabled
}
