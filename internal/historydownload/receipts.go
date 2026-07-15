package historydownload

import (
	"errors"
	"time"
)

type InstrumentSnapshotReceipt struct {
	SchemaVersion            string     `json:"schema_version"`
	Endpoint                 string     `json:"endpoint"`
	KnownFrom                time.Time  `json:"known_from"`
	PITStatus                string     `json:"pit_status"`
	HistoricalCatalogAllowed bool       `json:"historical_catalog_allowed"`
	Projection               Projection `json:"projection"`
}

func BuildInstrumentSnapshot(endpoint string, records []map[string]any, receiveTime time.Time) (InstrumentSnapshotReceipt, error) {
	if endpoint != PublicInstruments && endpoint != AccountInstruments {
		return InstrumentSnapshotReceipt{}, errors.New("historydownload: instrument endpoint rejected")
	}
	projection, err := Project(endpoint, records, TokenStore{})
	if err != nil {
		return InstrumentSnapshotReceipt{}, err
	}
	return InstrumentSnapshotReceipt{
		SchemaVersion: "historydownload.instrument-snapshot.v1", Endpoint: endpoint,
		KnownFrom: receiveTime, PITStatus: InstrumentPITStatus, HistoricalCatalogAllowed: false,
		Projection: projection,
	}, nil
}

type ManualExportManifest struct {
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status"`
	Imported      bool   `json:"imported"`
}

func NewManualExportManifest() ManualExportManifest {
	return ManualExportManifest{SchemaVersion: "historydownload.manual-export.v1", Status: ReportCenterStatus, Imported: false}
}
