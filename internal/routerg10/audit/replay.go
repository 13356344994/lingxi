package audit

// VerifyReplay compares two trace logs entry-by-entry for deterministic replay.
func VerifyReplay(expected, actual TraceLog) ReplayResult {
	result := ReplayResult{
		SchemaVersion: SchemaVersion,
		EntryCount:    len(expected.Entries),
		Match:         true,
	}
	if len(expected.Entries) != len(actual.Entries) {
		result.Match = false
		result.FirstMismatchAt = min(len(expected.Entries), len(actual.Entries))
		return result
	}
	for i := range expected.Entries {
		if expected.Entries[i].TraceHash != actual.Entries[i].TraceHash {
			result.Match = false
			result.FirstMismatchAt = i
			result.ExpectedHash = expected.Entries[i].TraceHash
			result.ActualHash = actual.Entries[i].TraceHash
			return result
		}
	}
	expHash, _ := HashLog(expected)
	actHash, _ := HashLog(actual)
	result.ExpectedHash = expHash
	result.ActualHash = actHash
	if expHash != actHash {
		result.Match = false
	}
	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
