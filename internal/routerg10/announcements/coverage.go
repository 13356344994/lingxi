package announcements

import (
	"fmt"
	"sort"
)

// RequiredSourceKeys returns preregistered cross-venue announcement archives.
func RequiredSourceKeys() []string {
	return []string{"okx", "binance", "bybit", "gate"}
}

// IsExplicitCoverageReceipt reports an explicit no-events or complete coverage receipt.
func IsExplicitCoverageReceipt(line ArchiveLine) bool {
	if line.CoverageReceipt == CoverageReceiptNoEventsComplete {
		return receiptTimeRangeComplete(line)
	}
	if line.Format != CoverageReceiptFormat {
		return false
	}
	return line.CoverageReceipt == CoverageReceiptNoEventsComplete && receiptTimeRangeComplete(line)
}

func receiptTimeRangeComplete(line ArchiveLine) bool {
	if line.CoverageFrom.IsZero() || line.CoverageTo.IsZero() {
		return false
	}
	return line.CoverageTo.After(line.CoverageFrom)
}

// BuildSourceStatus seals one venue archive pass without synthesizing history.
func BuildSourceStatus(path string, stats streamStats) SourceStatus {
	st := SourceStatus{
		Path:            path,
		Lines:           stats.Lines,
		Decoded:         stats.Decoded,
		Skipped:         stats.Skipped,
		Malformed:       stats.Malformed,
		Truncated:       stats.Truncated,
		PagesComplete:   stats.PagesComplete,
		PagesExpected:   stats.PagesExpected,
		FetchFailed:     stats.FetchFailed,
		CoverageReceipt: stats.CoverageReceipt,
		CoverageFrom:    stats.CoverageFrom,
		CoverageTo:      stats.CoverageTo,
		Status:          StatusAvailable,
	}
	switch {
	case stats.FetchFailed:
		st.Status = StatusIncomplete
		st.Reason = ReasonFetchFailed
	case stats.Truncated:
		st.Status = StatusTruncated
		st.Reason = ReasonTruncatedIngest
	case stats.PagesExpected > 0 && stats.PagesComplete < stats.PagesExpected:
		st.Status = StatusIncomplete
		st.Reason = ReasonPaginationIncomplete
	case stats.Decoded == 0 && !stats.CoverageReceiptComplete:
		st.Status = StatusIncomplete
		st.Reason = ReasonEmptyArchiveWithoutReceipt
	case stats.Decoded == 0 && stats.CoverageReceiptComplete:
		st.Status = StatusAvailable
		st.Reason = ReasonNoEventsCoverageReceipt
	}
	return st
}

// MissingSourceStatus marks a required venue archive that is absent on disk.
func MissingSourceStatus(path string) SourceStatus {
	return SourceStatus{
		Path:    path,
		Status:  StatusIncomplete,
		Missing: true,
		Reason:  ReasonRequiredVenueArchiveMissing,
	}
}

// ShouldFailClosed reports incomplete archives that must not backfill history.
func ShouldFailClosed(sourceStatus map[string]SourceStatus) bool {
	return len(CollectIncompleteReasons(sourceStatus)) > 0
}

// CollectIncompleteReasons aggregates fail-closed reasons across required venues.
func CollectIncompleteReasons(sourceStatus map[string]SourceStatus) []string {
	seen := map[string]struct{}{}
	var reasons []string
	add := func(reason string) {
		if reason == "" {
			return
		}
		if _, ok := seen[reason]; ok {
			return
		}
		seen[reason] = struct{}{}
		reasons = append(reasons, reason)
	}
	for _, key := range RequiredSourceKeys() {
		st, ok := sourceStatus[key]
		if !ok {
			add(fmt.Sprintf("%s:%s", key, ReasonRequiredVenueArchiveMissing))
			continue
		}
		if st.Missing {
			add(fmt.Sprintf("%s:%s", key, ReasonRequiredVenueArchiveMissing))
			continue
		}
		if st.FetchFailed || st.Status == StatusIncomplete || st.Status == StatusUnknown {
			add(fmt.Sprintf("%s:%s", key, firstNonEmpty(st.Reason, st.Status)))
			continue
		}
		if st.Truncated {
			add(fmt.Sprintf("%s:%s", key, firstNonEmpty(st.Reason, ReasonTruncatedIngest)))
			continue
		}
		if st.PagesExpected > 0 && st.PagesComplete < st.PagesExpected {
			add(fmt.Sprintf("%s:%s", key, firstNonEmpty(st.Reason, ReasonPaginationIncomplete)))
		}
	}
	sort.Strings(reasons)
	return reasons
}
