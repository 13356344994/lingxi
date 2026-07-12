package validation

import "lingxi/internal/routerg4/profitmap"

func cellKeyString(k profitmap.CellKey) string {
	return k.SkyCoarse + "|" + k.Liquidity + "|" + k.SymbolClass + "|" +
		k.Horizon + "|" + k.Direction + "|" + itoa(k.CapitalTier)
}

func indexKey(profileID string, key profitmap.CellKey) string {
	return profileID + "\x00" + cellKeyString(key)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	buf := make([]byte, 0, 12)
	for v > 0 {
		buf = append([]byte{byte('0' + v%10)}, buf...)
		v /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
