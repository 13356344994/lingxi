package announcements

import "path/filepath"

func allVenueSources(root, filename string) SourcePaths {
	path := filepath.Join(root, filename)
	return SourcePaths{
		OKX:     path,
		Binance: path,
		Bybit:   path,
		Gate:    path,
	}
}
