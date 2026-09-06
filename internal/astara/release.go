package astara

const (
	ImplementationVersion    = "0.1.0-astara.1"
	UpstreamBaseline         = "v0.8.0"
	UpstreamCommit           = "1edcd54b43606d9079bb36650efe3f68707a79ea"
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
	}
}
