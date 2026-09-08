package chatpipeline

import (
	"github.com/Tencent/WeKnora/internal/types"
	"testing"
)

func TestGraphResultMustRemainWithinResolvedDocumentTargets(t *testing.T) {
	targets := types.SearchTargets{nil, {Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "kb", KnowledgeIDs: []string{"allowed"}}}
	for _, result := range []*types.SearchResult{nil,
		{KnowledgeID: "private", KnowledgeBaseID: "kb"},
		{KnowledgeID: "allowed", KnowledgeBaseID: "wrong"},
	} {
		if resultWithinSearchTargets(targets, result) {
			t.Fatal("graph result escaped scope")
		}
	}
	if !resultWithinSearchTargets(targets, &types.SearchResult{KnowledgeID: "allowed", KnowledgeBaseID: "kb"}) {
		t.Fatal("allowed graph result rejected")
	}
}

func TestDocumentTargetRejectsForeignIndexResults(t *testing.T) {
	target := &types.SearchTarget{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "kb", KnowledgeIDs: []string{"allowed"}}
	good := &types.SearchResult{KnowledgeID: "allowed", KnowledgeBaseID: "kb", Content: "allowed"}
	results := []*types.SearchResult{nil, good,
		{KnowledgeID: "private", KnowledgeBaseID: "kb", Content: "private"},
		{KnowledgeID: "allowed", KnowledgeBaseID: "foreign", Content: "wrong mapping"},
		{KnowledgeID: "", KnowledgeBaseID: "kb", Content: "missing identity"},
	}
	got := restrictResultsToDocumentTarget(target, results)
	if len(got) != 1 || got[0] != good {
		t.Fatalf("document scope leaked: %+v", got)
	}
	target.KnowledgeIDs = nil
	if got := restrictResultsToDocumentTarget(target, results); len(got) != 0 {
		t.Fatal("empty scope widened")
	}
}
