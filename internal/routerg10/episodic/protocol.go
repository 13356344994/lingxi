// Package episodic implements G10 frozen historical similar-case memory.
// Episodes store state vector, event family, action path, and conservative
// outcome only; retrieval is capped at K=8 and forbids future-outcome query features.
package episodic

import g10protocol "lingxi/internal/routerg10/protocol"

const (
	SchemaVersionEpisode = "lingxi.routerg10.episodic.episode.v1"
	SchemaVersionIndex   = "lingxi.routerg10.episodic.index.v1"
	SchemaVersionAdvice  = "lingxi.routerg10.episodic.advice.v1"

	Generation = g10protocol.Generation

	MaxRetrievalK = g10protocol.EpisodicMaxRetrievalK

	MinSampleForFullSize = 3
	NegativeMajorityFrac = 0.5

	AdviceVetoOpen           = "MEMORY_NEGATIVE_MAJORITY_VETO"
	AdviceInsufficientSample = "MEMORY_INSUFFICIENT_SAMPLE"
	AdviceReduceSize         = "MEMORY_REDUCE_SIZE"
	AdviceNoVeto             = "MEMORY_NO_VETO"

	RejectForbiddenQueryFeature = "EPISODIC_FORBIDDEN_QUERY_FEATURE"

	ResidualDefaultStatus = g10protocol.ResidualDefaultStatus
	ResidualMaxParameters = g10protocol.ResidualMaxParameters
)

// ForbiddenQueryFields mirrors protocol.EpisodicForbiddenQueryFeatures.
var ForbiddenQueryFields = append([]string(nil), g10protocol.EpisodicForbiddenQueryFeatures...)

// SimilarityFeatureNames mirrors protocol.EpisodicSimilarityFeatures.
var SimilarityFeatureNames = append([]string(nil), g10protocol.EpisodicSimilarityFeatures...)
