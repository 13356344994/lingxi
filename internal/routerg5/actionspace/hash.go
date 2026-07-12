package actionspace

import "lingxi/internal/datatruth"

func hashPayload(payload any) (string, error) {
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}
