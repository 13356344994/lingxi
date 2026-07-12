package validation

import (
	"lingxi/internal/routerg4/profitmap"
	"lingxi/internal/routerg4/reversemine"
)

// CandidateIndex maps profile+cell to candidate indices for O(bucket) lookup.
type CandidateIndex struct {
	byKey           map[string][]int
	keysByProfile   map[string][]profitmap.CellKey
	totalCandidates int
}

// BuildCandidateIndex indexes frozen candidates by profile and cell dimensions.
func BuildCandidateIndex(candidates []reversemine.Candidate) CandidateIndex {
	idx := CandidateIndex{
		byKey:           map[string][]int{},
		keysByProfile:   map[string][]profitmap.CellKey{},
		totalCandidates: len(candidates),
	}
	seenProfileKey := map[string]map[string]bool{}
	for i, c := range candidates {
		key := indexKey(c.ProfileID, c.CellKey)
		idx.byKey[key] = append(idx.byKey[key], i)
		if seenProfileKey[c.ProfileID] == nil {
			seenProfileKey[c.ProfileID] = map[string]bool{}
		}
		ck := cellKeyString(c.CellKey)
		if !seenProfileKey[c.ProfileID][ck] {
			seenProfileKey[c.ProfileID][ck] = true
			idx.keysByProfile[c.ProfileID] = append(idx.keysByProfile[c.ProfileID], c.CellKey)
		}
	}
	return idx
}

// Lookup returns candidate indices for one profile+cell key.
func (idx CandidateIndex) Lookup(profileID string, key profitmap.CellKey) []int {
	return idx.byKey[indexKey(profileID, key)]
}

// KeysForProfile returns unique cell keys with candidates for one profile.
func (idx CandidateIndex) KeysForProfile(profileID string) []profitmap.CellKey {
	return idx.keysByProfile[profileID]
}

// TotalCandidates returns indexed candidate count.
func (idx CandidateIndex) TotalCandidates() int {
	return idx.totalCandidates
}

// BucketSize returns number of candidates in one bucket.
func (idx CandidateIndex) BucketSize(profileID string, key profitmap.CellKey) int {
	return len(idx.Lookup(profileID, key))
}

// MaxBucketSize returns largest bucket size across all keys.
func (idx CandidateIndex) MaxBucketSize() int {
	max := 0
	for _, bucket := range idx.byKey {
		if len(bucket) > max {
			max = len(bucket)
		}
	}
	return max
}
