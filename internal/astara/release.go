package astara

const (
	ImplementationVersion = "0.8.2-astara.1"
	// UpstreamBaseline is the upstream release this fork's main is synced to,
	// and UpstreamCommit is the exact upstream main commit merged into it.
	// Upstream tagged v0.8.2 at 3e8b0bfc; main has since advanced past the
	// tag on the v0.8.2 development line, and the merged commit is the
	// authoritative anchor.
	UpstreamBaseline         = "v0.8.2"
	UpstreamCommit           = "bccb4b151bae403508da77fbb174efc79dc47c1a"
	APIContractVersion       = 1
	UIContractVersion        = 1
	SourceContractVersion    = 1
	ToolContractVersion      = 1
	ReadinessContractVersion = 1
	MigrationContractVersion = 1
)

type Identity struct {
	ImplementationVersion    string `json:"implementation_version"`
	UpstreamBaseline         string `json:"upstream_baseline"`
	UpstreamCommit           string `json:"upstream_commit"`
	FeatureProfile           string `json:"feature_profile"`
	FeatureProfileDigest     string `json:"feature_profile_digest"`
	APIContractVersion       int    `json:"api_contract_version"`
	UIContractVersion        int    `json:"ui_contract_version"`
	SourceContractVersion    int    `json:"source_contract_version"`
	ToolContractVersion      int    `json:"tool_contract_version"`
	ReadinessContractVersion int    `json:"readiness_contract_version"`
	MigrationVersion         int    `json:"migration_version"`
	MigrationPosition        int    `json:"migration_position"`
}

func ReleaseIdentity(profile Profile) Identity {
	return Identity{
		ImplementationVersion:    ImplementationVersion,
		UpstreamBaseline:         UpstreamBaseline,
		UpstreamCommit:           UpstreamCommit,
		FeatureProfile:           profile.Name,
		FeatureProfileDigest:     ProfileDigest(profile),
		APIContractVersion:       APIContractVersion,
		UIContractVersion:        UIContractVersion,
		SourceContractVersion:    SourceContractVersion,
		ToolContractVersion:      ToolContractVersion,
		ReadinessContractVersion: ReadinessContractVersion,
		MigrationVersion:         MigrationContractVersion,
		MigrationPosition:        MigrationContractVersion,
	}
}
