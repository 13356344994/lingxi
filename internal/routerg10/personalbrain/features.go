package personalbrain

import (
	"lingxi/internal/routerg10/announcements"
)

// EnrichBarFeatures fills decision-visible trigger fields from bar input (PIT announcements, shock cluster).
func EnrichBarFeatures(feat BarFeatures, in BarInput) BarFeatures {
	out := feat
	if out.VisibleAnnouncementCount == 0 && len(in.Announcements) > 0 {
		visible := announcements.VisibleAnnouncements(in.Announcements, EventDecisionTime(in.DecisionTime))
		out.VisibleAnnouncementCount = len(visible)
	}
	if out.CrossVenueShockCluster == "" {
		if in.ShockCluster != "" {
			out.CrossVenueShockCluster = in.ShockCluster
		}
	}
	return out
}
