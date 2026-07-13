package personalvalidation

import (
	"time"

	"lingxi/internal/routerg10/announcements"
)

func evaluatePITFlags(all []announcements.Announcement, decision time.Time) (publishedContaminated, firstSeenContaminated bool) {
	for _, a := range all {
		if a.PublishedAt.After(decision) {
			if announcements.VisibleAt(a, decision) {
				publishedContaminated = true
			}
		}
		if a.FirstSeenAt.After(decision) {
			if announcements.VisibleAt(a, decision) {
				firstSeenContaminated = true
			}
		}
		if !a.EffectiveAt.IsZero() && a.EffectiveAt.After(decision) {
			if announcements.VisibleAt(a, decision) {
				publishedContaminated = true
			}
		}
	}
	return publishedContaminated, firstSeenContaminated
}

// BuildPITReport aggregates PIT contamination tests across all triggers.
func BuildPITReport(events []TriggerEvent) PITContaminationReport {
	report := PITContaminationReport{TriggersTested: len(events)}
	for _, ev := range events {
		if ev.PITPublishedContaminated {
			report.PublishedLookaheadFails++
		}
		if ev.PITFirstSeenContaminated {
			report.FirstSeenLookaheadFails++
		}
	}
	report.ContaminationDetected = report.PublishedLookaheadFails > 0 || report.FirstSeenLookaheadFails > 0
	return report
}

// RunPITContaminationSelfTest verifies announcement visibility rules on fixture decisions.
func RunPITContaminationSelfTest() PITContaminationReport {
	pub := time.Date(2024, 6, 20, 8, 0, 0, 0, time.UTC)
	firstSeen := time.Date(2024, 6, 20, 9, 0, 0, 0, time.UTC)
	effective := time.Date(2024, 6, 25, 0, 0, 0, 0, time.UTC)
	decision := time.Date(2024, 6, 10, 0, 0, 0, 0, time.UTC)
	a := announcements.Announcement{
		Venue: announcements.VenueBinance, Symbol: "ETH-USDT", Kind: announcements.KindListing,
		PublishedAt: pub, FirstSeenAt: firstSeen, EffectiveAt: effective, SourceHash: "test", InfoOnly: true,
	}
	pubBad, fsBad := evaluatePITFlags([]announcements.Announcement{a}, decision)
	ev := TriggerEvent{DecisionTime: decision, PITPublishedContaminated: pubBad, PITFirstSeenContaminated: fsBad}
	return BuildPITReport([]TriggerEvent{ev})
}
