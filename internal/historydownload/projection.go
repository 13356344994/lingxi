package historydownload

import (
	"errors"
	"sort"
)

const (
	OrdersEndpoint      = "/api/v5/trade/orders-history-archive"
	FillsEndpoint       = "/api/v5/trade/fills-history"
	PublicInstruments   = "/api/v5/public/instruments"
	AccountInstruments  = "/api/v5/account/instruments"
	MarketDataDiscovery = "/api/v5/public/market-data-history"
)

var projectionSchemas = map[string]map[string]bool{
	OrdersEndpoint: {
		"ordId": true, "clOrdId": true, "instId": true, "instType": true,
		"ordType": true, "side": true, "posSide": true, "px": true, "sz": true,
		"accFillSz": true, "state": true, "cTime": true, "uTime": true,
		"fee": true, "feeCcy": true, "category": true, "source": true,
	},
	FillsEndpoint: {
		"tradeId": true, "ordId": true, "clOrdId": true, "billId": true,
		"instId": true, "instType": true, "fillPx": true, "fillSz": true,
		"fillTime": true, "ts": true, "execType": true, "fee": true, "feeCcy": true,
		"side": true, "posSide": true,
	},
	PublicInstruments: {
		"instId": true, "instType": true, "ctType": true, "minSz": true,
		"ctVal": true, "ctValCcy": true, "lotSz": true, "tickSz": true,
		"state": true, "listTime": true,
	},
	AccountInstruments: {
		"instId": true, "instType": true, "ctType": true, "minSz": true,
		"ctVal": true, "ctValCcy": true, "lotSz": true, "tickSz": true,
		"state": true, "listTime": true,
	},
}

var tokenFields = map[string]bool{
	"ordId": true, "clOrdId": true, "tradeId": true, "billId": true,
}

type Projection struct {
	Records        []map[string]any `json:"records"`
	Fields         []string         `json:"fields"`
	SchemaHash     string           `json:"schema_hash"`
	PresenceCounts map[string]int   `json:"presence_counts"`
	OmittedCounts  map[string]int   `json:"omitted_counts,omitempty"`
}

func ProjectionSchema(endpoint string) ([]string, string, error) {
	if projectionSchemas[endpoint] == nil {
		return nil, "", errors.New("historydownload: endpoint has no projection schema")
	}
	fields := projectionFields(endpoint)
	schemaHash, err := hash(fields)
	return fields, schemaHash, err
}

func Fingerprint(v any) (string, error) { return hash(v) }

// Project applies an endpoint-specific allowlist. Unknown and forbidden fields
// remain only in raw evidence and are counted as omitted without exposing
// values; raw identifiers admitted to projection become local HMAC tokens.
func Project(endpoint string, records []map[string]any, tokens TokenStore) (Projection, error) {
	schema, ok := projectionSchemas[endpoint]
	if !ok {
		return Projection{}, errors.New("historydownload: endpoint has no projection schema")
	}
	fields := make([]string, 0, len(schema))
	for field := range schema {
		if tokenFields[field] {
			fields = append(fields, field+"Token")
		} else {
			fields = append(fields, field)
		}
	}
	sort.Strings(fields)
	schemaHash, err := hash(fields)
	if err != nil {
		return Projection{}, err
	}
	result := Projection{
		Fields: fields, SchemaHash: schemaHash, PresenceCounts: map[string]int{}, OmittedCounts: map[string]int{},
	}
	for _, record := range records {
		projected := map[string]any{}
		for field, value := range record {
			if !schema[field] {
				// Raw retains upstream evidence. The research projection must
				// not fail a legal expanded OKX response or expose outcomes.
				result.OmittedCounts[field]++
				continue
			}
			outputField := field
			if tokenFields[field] {
				text, ok := value.(string)
				if !ok || text == "" {
					continue
				}
				token, err := tokens.Token(text)
				if err != nil {
					return Projection{}, err
				}
				outputField = field + "Token"
				value = token
			}
			projected[outputField] = value
			result.PresenceCounts[outputField]++
		}
		result.Records = append(result.Records, projected)
	}
	return result, nil
}
