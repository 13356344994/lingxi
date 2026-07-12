package announcements

import "time"

// VisibleAt reports whether an announcement was knowable at decisionTime.
// Strict PIT: first_seen_at <= decision AND published_at <= decision.
// Web page update times, future effective dates, and current-universe backfill
// must not make an announcement visible before these bounds.
func VisibleAt(a Announcement, decisionTime time.Time) bool {
	if decisionTime.IsZero() {
		return false
	}
	if a.FirstSeenAt.IsZero() || a.PublishedAt.IsZero() {
		return false
	}
	fs := a.FirstSeenAt.UTC()
	pub := a.PublishedAt.UTC()
	dec := decisionTime.UTC()
	if fs.After(dec) {
		return false
	}
	if pub.After(dec) {
		return false
	}
	return true
}

// IsEffectiveAt reports whether the announcement effect has started by decisionTime.
// Future effective_at may be visible as announced information but is not effective yet.
func IsEffectiveAt(a Announcement, decisionTime time.Time) bool {
	if !VisibleAt(a, decisionTime) {
		return false
	}
	if a.EffectiveAt.IsZero() {
		return false
	}
	return !a.EffectiveAt.UTC().After(decisionTime.UTC())
}

// HasFutureEffective reports visible-but-not-yet-effective announcements.
func HasFutureEffective(a Announcement, decisionTime time.Time) bool {
	if !VisibleAt(a, decisionTime) {
		return false
	}
	if a.EffectiveAt.IsZero() {
		return false
	}
	return a.EffectiveAt.UTC().After(decisionTime.UTC())
}

// VisibleAnnouncements returns PIT-visible announcements at decisionTime sorted by first_seen.
func VisibleAnnouncements(all []Announcement, decisionTime time.Time) []Announcement {
	var out []Announcement
	for _, a := range all {
		if VisibleAt(a, decisionTime) {
			out = append(out, a)
		}
	}
	sortAnnouncements(out)
	return out
}
