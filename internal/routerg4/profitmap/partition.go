package profitmap

import (
	"errors"
	"fmt"
	"time"

	"lingxi/internal/datatruth"
	"lingxi/internal/routerg4/protocol"
)

func partitionTime(start time.Time, span time.Duration, permille int) time.Time {
	offset := span / time.Duration(1000) * time.Duration(permille)
	return start.Add(offset)
}

// ComputeBoundaries builds per-profile 60/20/20 partitions with purge/embargo gaps.
func ComputeBoundaries(spec ProfileSpec, bars []Bar) (Boundaries, error) {
	if len(bars) < 3 {
		return Boundaries{}, errors.New("profitmap: insufficient BTC history for partitions")
	}
	start := bars[0].OpenTime
	end := bars[len(bars)-1].OpenTime.Add(spec.BarDuration)
	span := end.Sub(start)
	embargo := MaxPurgeEmbargo(spec)
	b := Boundaries{
		ProfileID:     spec.ID,
		Start:         start,
		DiscoveryEnd:  partitionTime(start, span, protocol.DiscoveryPermille),
		ValidationEnd: partitionTime(start, span, protocol.DiscoveryPermille+protocol.ValidationPermille),
		End:           end,
		PurgeEmbargo:  embargo,
	}
	b.ValidationStart = b.DiscoveryEnd.Add(embargo)
	b.WFStart = b.ValidationEnd.Add(embargo)
	if !b.ValidationStart.Before(b.ValidationEnd) {
		return Boundaries{}, fmt.Errorf("profitmap: profile %q span cannot satisfy validation embargo", spec.ID)
	}
	if !b.WFStart.Before(b.End) {
		return Boundaries{}, fmt.Errorf("profitmap: profile %q span cannot satisfy walk-forward embargo", spec.ID)
	}
	return b, nil
}

// HashBoundaries returns a tamper-evident hash of partition boundaries.
func HashBoundaries(b Boundaries) (string, error) {
	hash, _, err := datatruth.HashJSON(b)
	return hash, err
}

// InDiscovery reports whether decisionTime falls in the discovery slice.
func InDiscovery(b Boundaries, decisionTime time.Time) bool {
	return !decisionTime.Before(b.Start) && decisionTime.Before(b.DiscoveryEnd)
}

// PartitionAt returns the partition label for a decision timestamp.
func (b Boundaries) PartitionAt(t time.Time) string {
	if t.Before(b.Start) || t.After(b.End) {
		return ""
	}
	if t.Before(b.DiscoveryEnd) {
		return PartitionDiscovery
	}
	if !t.Before(b.ValidationStart) && t.Before(b.ValidationEnd) {
		return PartitionValidation
	}
	if !t.Before(b.WFStart) && !t.After(b.End) {
		return PartitionWalkFwd
	}
	return ""
}
