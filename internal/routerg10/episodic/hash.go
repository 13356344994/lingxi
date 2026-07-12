package episodic

import (
	"lingxi/internal/datatruth"
)

// HashEpisode returns canonical hash for one frozen episode (episode_hash field cleared).
func HashEpisode(ep FrozenEpisode) (string, error) {
	payload := ep
	payload.EpisodeHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

// HashIndex returns canonical hash for one memory index (index_hash field cleared).
func HashIndex(idx MemoryIndex) (string, error) {
	payload := idx
	payload.IndexHash = ""
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}
