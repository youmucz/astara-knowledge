// Package astara defines the closed runtime boundary used by Astara Plane.
package astara

import (
	"os"
	"sort"
	"strings"
)

const (
	ProfileEnv       = "WEKNORA_FEATURE_PROFILE"
	KnowledgeProfile = "astara-knowledge"
)

type Feature string

const (
	FeatureAuth            Feature = "auth"
	FeatureTenants         Feature = "tenants"
	FeatureKnowledgeBases  Feature = "knowledge_bases"
	FeatureKnowledge       Feature = "knowledge"
	FeatureModels          Feature = "models"
	FeatureStorage         Feature = "storage"
	FeatureVectorStore     Feature = "vector_store"
	FeatureDataSources     Feature = "data_sources"
	FeatureAgent           Feature = "agent"
	FeatureSkills          Feature = "skills"
	FeatureSandbox         Feature = "sandbox"
	FeatureMCP             Feature = "mcp"
	FeatureWebSearch       Feature = "web_search"
	FeatureIM              Feature = "im"
	FeatureMemory          Feature = "memory"
	FeatureDataAnalysis    Feature = "data_analysis"
	FeatureExecutableTools Feature = "executable_tools"
	FeatureGraphRAG        Feature = "graph_rag"
)

var knowledgeFeatures = map[Feature]struct{}{
	FeatureAuth: {}, FeatureTenants: {}, FeatureKnowledgeBases: {},
	FeatureKnowledge: {}, FeatureModels: {}, FeatureStorage: {},
	FeatureVectorStore: {}, FeatureDataSources: {},
}

var prohibitedFeatures = []Feature{
	FeatureAgent, FeatureSkills, FeatureSandbox, FeatureMCP, FeatureWebSearch,
	FeatureIM, FeatureMemory, FeatureDataAnalysis, FeatureExecutableTools, FeatureGraphRAG,
}

// Profile is deliberately closed: unknown names never inherit the upstream
// feature set and are reported as invalid by readiness.
type Profile struct {
	Name  string
	Valid bool
}

func CurrentProfile() Profile {
	name := strings.TrimSpace(os.Getenv(ProfileEnv))
	if name == "" {
		name = KnowledgeProfile
	}
	return Profile{Name: name, Valid: name == KnowledgeProfile}
}

func (p Profile) Enabled(feature Feature) bool {
	if !p.Valid {
		return false
	}
	_, ok := knowledgeFeatures[feature]
	return ok
}

func (p Profile) Features() []Feature {
	result := make([]Feature, 0, len(knowledgeFeatures))
	if !p.Valid {
		return result
	}
	for feature := range knowledgeFeatures {
		result = append(result, feature)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func ProhibitedFeatures() []Feature {
	return append([]Feature(nil), prohibitedFeatures...)
}
