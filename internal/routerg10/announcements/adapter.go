package announcements

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Adapter converts one archive line into normalized announcements.
type Adapter interface {
	Venue() Venue
	Adapt(line ArchiveLine) ([]Announcement, error)
}

var adapters = map[Venue]Adapter{
	VenueOKX:     okxAdapter{},
	VenueBinance: binanceAdapter{},
	VenueBybit:   bybitAdapter{},
	VenueGate:    gateAdapter{},
}

// AdaptArchiveLine routes one archive record through the venue adapter.
func AdaptArchiveLine(line ArchiveLine, venue Venue) ([]Announcement, error) {
	if line.Format == "normalized_v1" {
		return adaptNormalized(line, venue)
	}
	ad, ok := adapters[venue]
	if !ok {
		return nil, fmt.Errorf("announcements: unsupported venue %q", venue)
	}
	return ad.Adapt(line)
}

func adaptNormalized(line ArchiveLine, venue Venue) ([]Announcement, error) {
	if len(line.Symbols) == 0 {
		return nil, fmt.Errorf("announcements: normalized line missing symbols")
	}
	hash := line.SourceHash
	if hash == "" {
		hash = line.BodyHash
	}
	if hash == "" {
		return nil, fmt.Errorf("announcements: normalized line missing source_hash")
	}
	firstSeen := line.FirstSeenAt
	if firstSeen.IsZero() && !line.FetchedAt.IsZero() {
		firstSeen = line.FetchedAt
	}
	var out []Announcement
	for _, sym := range line.Symbols {
		a := Announcement{
			Venue:         venue,
			Symbol:        sym,
			Kind:          line.Kind,
			PublishedAt:   line.PublishedAt.UTC(),
			FirstSeenAt:   firstSeen.UTC(),
			EffectiveAt:   line.EffectiveAt.UTC(),
			SourceURL:     line.SourceURL,
			SourceHash:    hash,
			Title:         line.Title,
			ExecutionVenue: ExecutionVenue,
			InfoOnly:      venue != VenueOKX,
		}
		if err := ValidateAnnouncement(a); err != nil {
			return nil, err
		}
		a.ID = DedupeKey(a)
		out = append(out, a)
	}
	return out, nil
}

// --- OKX ---

type okxAdapter struct{}

func (okxAdapter) Venue() Venue { return VenueOKX }

func (okxAdapter) Adapt(line ArchiveLine) ([]Announcement, error) {
	if len(line.Body) == 0 {
		return adaptNormalized(line, VenueOKX)
	}
	details, err := parseOKXBody(line.Body)
	if err != nil {
		return nil, err
	}
	hash := line.BodyHash
	if hash == "" {
		hash = line.SourceHash
	}
	firstSeen := line.FetchedAt.UTC()
	if firstSeen.IsZero() {
		firstSeen = time.Now().UTC()
	}
	var out []Announcement
	for _, d := range details {
		kind := okxKind(d.AnnType, d.Title)
		if kind == "" {
			continue
		}
		symbols := extractOKXSymbols(d.Title)
		if len(symbols) == 0 {
			continue
		}
		pub := parseOKXMillis(d.PTime)
		for _, sym := range symbols {
			a := Announcement{
				Venue:          VenueOKX,
				Symbol:         sym,
				Kind:           kind,
				PublishedAt:    pub,
				FirstSeenAt:    firstSeen,
				EffectiveAt:    parseEffectiveFromTitle(d.Title, pub),
				SourceURL:      firstNonEmpty(d.URL, line.SourceURL),
				SourceHash:     hash,
				Title:          d.Title,
				ExecutionVenue: ExecutionVenue,
				InfoOnly:       false,
			}
			a.ID = DedupeKey(a)
			out = append(out, a)
		}
	}
	return out, nil
}

type okxDetail struct {
	AnnType string
	PTime   string
	Title   string
	URL     string
}

func parseOKXBody(body json.RawMessage) ([]okxDetail, error) {
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			Details []okxDetail `json:"details"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Code != "" && resp.Code != "0" {
		return nil, fmt.Errorf("announcements: okx code %q", resp.Code)
	}
	var out []okxDetail
	for _, block := range resp.Data {
		out = append(out, block.Details...)
	}
	return out, nil
}

var okxPairRe = regexp.MustCompile(`([A-Z0-9]{2,15})-USDT`)

func extractOKXSymbols(title string) []string {
	upper := strings.ToUpper(strings.TrimSpace(title))
	seen := map[string]struct{}{}
	var out []string
	for _, m := range okxPairRe.FindAllStringSubmatch(upper, -1) {
		if len(m) < 2 {
			continue
		}
		for _, sym := range []string{m[1] + "-USDT", m[1]} {
			if _, ok := seen[sym]; ok {
				continue
			}
			seen[sym] = struct{}{}
			out = append(out, sym)
		}
	}
	return out
}

func okxKind(annType, title string) Kind {
	switch strings.TrimSpace(annType) {
	case "announcements-delistings":
		return KindDelisting
	case "announcements-new-listings":
		return KindListing
	case "announcements-system-maintenance-or-update":
		return KindMaintenance
	}
	lower := strings.ToLower(title)
	switch {
	case strings.Contains(lower, "delist"):
		return KindDelisting
	case strings.Contains(lower, "list"):
		return KindListing
	case strings.Contains(lower, "maintenance"):
		return KindMaintenance
	case strings.Contains(lower, "suspend") || strings.Contains(title, "暂停"):
		return KindSuspension
	}
	return ""
}

func parseOKXMillis(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// --- Binance ---

type binanceAdapter struct{}

func (binanceAdapter) Venue() Venue { return VenueBinance }

func (binanceAdapter) Adapt(line ArchiveLine) ([]Announcement, error) {
	if len(line.Body) == 0 {
		return adaptNormalized(line, VenueBinance)
	}
	var rows []struct {
		Title       string `json:"title"`
		Code        string `json:"code"`
		PublishDate int64  `json:"publishDate"`
	}
	if err := json.Unmarshal(line.Body, &rows); err != nil {
		var wrapped struct {
			Data []struct {
				Title       string `json:"title"`
				Code        string `json:"code"`
				PublishDate int64  `json:"publishDate"`
			} `json:"data"`
		}
		if err2 := json.Unmarshal(line.Body, &wrapped); err2 != nil {
			return nil, err
		}
		for _, d := range wrapped.Data {
			rows = append(rows, struct {
				Title       string `json:"title"`
				Code        string `json:"code"`
				PublishDate int64  `json:"publishDate"`
			}{Title: d.Title, Code: d.Code, PublishDate: d.PublishDate})
		}
	}
	hash := firstNonEmpty(line.BodyHash, line.SourceHash)
	firstSeen := line.FetchedAt.UTC()
	var out []Announcement
	for _, row := range rows {
		kind := binanceKind(row.Title)
		if kind == "" {
			continue
		}
		symbols := extractGenericSymbols(row.Title)
		pub := time.UnixMilli(row.PublishDate).UTC()
		url := firstNonEmpty(row.Code, line.SourceURL)
		for _, sym := range symbols {
			a := Announcement{
				Venue:          VenueBinance,
				Symbol:         sym,
				Kind:           kind,
				PublishedAt:    pub,
				FirstSeenAt:    firstSeen,
				EffectiveAt:    parseEffectiveFromTitle(row.Title, pub),
				SourceURL:      url,
				SourceHash:     hash,
				Title:          row.Title,
				ExecutionVenue: ExecutionVenue,
				InfoOnly:       true,
			}
			a.ID = DedupeKey(a)
			out = append(out, a)
		}
	}
	return out, nil
}

func binanceKind(title string) Kind {
	lower := strings.ToLower(title)
	switch {
	case strings.Contains(lower, "delist"):
		return KindDelisting
	case strings.Contains(lower, "will list") || strings.Contains(lower, "lists "):
		return KindListing
	case strings.Contains(lower, "maintenance"):
		return KindMaintenance
	case strings.Contains(lower, "suspend"):
		return KindSuspension
	}
	return ""
}

// --- Bybit ---

type bybitAdapter struct{}

func (bybitAdapter) Venue() Venue { return VenueBybit }

func (bybitAdapter) Adapt(line ArchiveLine) ([]Announcement, error) {
	if len(line.Body) == 0 {
		return adaptNormalized(line, VenueBybit)
	}
	var resp struct {
		Result struct {
			List []struct {
				Title     string `json:"title"`
				URL       string `json:"url"`
				PublishTime int64 `json:"publishTime"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line.Body, &resp); err != nil {
		return nil, err
	}
	hash := firstNonEmpty(line.BodyHash, line.SourceHash)
	firstSeen := line.FetchedAt.UTC()
	var out []Announcement
	for _, row := range resp.Result.List {
		kind := bybitKind(row.Title)
		if kind == "" {
			continue
		}
		symbols := extractGenericSymbols(row.Title)
		pub := time.UnixMilli(row.PublishTime).UTC()
		for _, sym := range symbols {
			a := Announcement{
				Venue:          VenueBybit,
				Symbol:         sym,
				Kind:           kind,
				PublishedAt:    pub,
				FirstSeenAt:    firstSeen,
				EffectiveAt:    parseEffectiveFromTitle(row.Title, pub),
				SourceURL:      firstNonEmpty(row.URL, line.SourceURL),
				SourceHash:     hash,
				Title:          row.Title,
				ExecutionVenue: ExecutionVenue,
				InfoOnly:       true,
			}
			a.ID = DedupeKey(a)
			out = append(out, a)
		}
	}
	return out, nil
}

func bybitKind(title string) Kind {
	lower := strings.ToLower(title)
	switch {
	case strings.Contains(lower, "delist"):
		return KindDelisting
	case strings.Contains(lower, "list"):
		return KindListing
	case strings.Contains(lower, "maintenance"):
		return KindMaintenance
	case strings.Contains(lower, "suspend"):
		return KindSuspension
	}
	return ""
}

// --- Gate ---

type gateAdapter struct{}

func (gateAdapter) Venue() Venue { return VenueGate }

func (gateAdapter) Adapt(line ArchiveLine) ([]Announcement, error) {
	if len(line.Body) == 0 {
		return adaptNormalized(line, VenueGate)
	}
	var rows []struct {
		Title     string `json:"title"`
		URL       string `json:"url"`
		Published int64  `json:"published_at"`
	}
	if err := json.Unmarshal(line.Body, &rows); err != nil {
		var wrapped struct {
			List []struct {
				Title     string `json:"title"`
				URL       string `json:"url"`
				Published int64  `json:"published_at"`
			} `json:"list"`
		}
		if err2 := json.Unmarshal(line.Body, &wrapped); err2 != nil {
			return nil, err
		}
		for _, d := range wrapped.List {
			rows = append(rows, struct {
				Title     string `json:"title"`
				URL       string `json:"url"`
				Published int64  `json:"published_at"`
			}{Title: d.Title, URL: d.URL, Published: d.Published})
		}
	}
	hash := firstNonEmpty(line.BodyHash, line.SourceHash)
	firstSeen := line.FetchedAt.UTC()
	var out []Announcement
	for _, row := range rows {
		kind := gateKind(row.Title)
		if kind == "" {
			continue
		}
		symbols := extractGenericSymbols(row.Title)
		pub := time.Unix(row.Published, 0).UTC()
		if row.Published > 1_000_000_000_000 {
			pub = time.UnixMilli(row.Published).UTC()
		}
		for _, sym := range symbols {
			a := Announcement{
				Venue:          VenueGate,
				Symbol:         sym,
				Kind:           kind,
				PublishedAt:    pub,
				FirstSeenAt:    firstSeen,
				EffectiveAt:    parseEffectiveFromTitle(row.Title, pub),
				SourceURL:      firstNonEmpty(row.URL, line.SourceURL),
				SourceHash:     hash,
				Title:          row.Title,
				ExecutionVenue: ExecutionVenue,
				InfoOnly:       true,
			}
			a.ID = DedupeKey(a)
			out = append(out, a)
		}
	}
	return out, nil
}

func gateKind(title string) Kind {
	lower := strings.ToLower(title)
	switch {
	case strings.Contains(lower, "delist"):
		return KindDelisting
	case strings.Contains(lower, "list"):
		return KindListing
	case strings.Contains(lower, "maintenance"):
		return KindMaintenance
	case strings.Contains(lower, "suspend"):
		return KindSuspension
	}
	return ""
}

var genericPairRe = regexp.MustCompile(`([A-Z0-9]{2,15})[-_/]?USDT`)

func extractGenericSymbols(title string) []string {
	upper := strings.ToUpper(strings.TrimSpace(title))
	seen := map[string]struct{}{}
	var out []string
	for _, m := range genericPairRe.FindAllStringSubmatch(upper, -1) {
		if len(m) < 2 {
			continue
		}
		sym := m[1] + "-USDT"
		if _, ok := seen[sym]; ok {
			continue
		}
		seen[sym] = struct{}{}
		out = append(out, sym)
	}
	return out
}

var effectiveDateRe = regexp.MustCompile(`(?i)(?:on|at|from)\s+(\d{4}[-/]\d{2}[-/]\d{2})`)

func parseEffectiveFromTitle(title string, fallback time.Time) time.Time {
	m := effectiveDateRe.FindStringSubmatch(title)
	if len(m) < 2 {
		return time.Time{}
	}
	raw := strings.ReplaceAll(m[1], "/", "-")
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}
	}
	if t.Before(fallback) {
		return time.Time{}
	}
	return t.UTC()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
