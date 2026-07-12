package eventaccount

// OverlapReject returns true when candidate overlaps an open/pending event or symbol window.
func OverlapReject(account *Account, c EventCandidate) (reason string, reject bool) {
	if _, ok := account.Slots[c.EventID]; ok {
		return RejectSameEvent, true
	}
	for _, slot := range account.OpenSlots() {
		if slot.Candidate.EventID == c.EventID {
			return RejectSameEvent, true
		}
		if slot.Candidate.Symbol != c.Symbol {
			continue
		}
		if windowsOverlap(slot.Candidate, c) {
			return RejectSymbolOverlap, true
		}
	}
	if held, ok := account.SymbolEvents[c.Symbol]; ok && held != "" {
		if slot, ok := account.Slots[held]; ok && !slot.Closed {
			if windowsOverlap(slot.Candidate, c) {
				return RejectSymbolOverlap, true
			}
		}
	}
	return "", false
}

func windowsOverlap(a, b EventCandidate) bool {
	if a.Symbol != b.Symbol {
		return false
	}
	aStart, aEnd := a.DecisionBarIndex, a.WindowEndBarIndex
	bStart, bEnd := b.DecisionBarIndex, b.WindowEndBarIndex
	if aEnd <= 0 {
		aEnd = a.DeadlineBarIndex
	}
	if bEnd <= 0 {
		bEnd = b.DeadlineBarIndex
	}
	if aEnd <= 0 {
		aEnd = aStart
	}
	if bEnd <= 0 {
		bEnd = bStart
	}
	return aStart <= bEnd && bStart <= aEnd
}
