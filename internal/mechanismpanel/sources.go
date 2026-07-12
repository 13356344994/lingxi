package mechanismpanel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/datatruth"
	contextdata "lingxi/internal/datatruth/context"
)

// Source 是流式 join 引擎的统一输入接口。实现必须逐行/逐条读取,不得把整个文件载入内存,
// 以满足 bounded memory 要求;LinesRead 用于 checkpoint 落盘续读位置。
type Source interface {
	Name() string
	Kind() Kind
	// Next 返回下一条记录;ok=false 且 err=nil 表示该源已到 EOF。
	Next() (Record, bool, error)
	LinesRead() int64
	Close() error
}

// clockFunc 供测试注入确定性的 process-time 时钟。
type clockFunc func() time.Time

// jsonlSource 是逐行 JSONL 读取器,skip 用于 resume(跳过 checkpoint 中已确认处理过的行数)。
type jsonlSource struct {
	name   string
	kind   Kind
	file   *os.File
	sc     *bufio.Scanner
	lineNo int64
	skip   int64
	decode func(raw []byte, seq int64, now time.Time) (Record, error)
	now    clockFunc
}

func newJSONLSource(name string, kind Kind, path string, skipLines int64, now clockFunc, decode func([]byte, int64, time.Time) (Record, error)) (*jsonlSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("mechanismpanel: open %s: %w", path, err)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	if now == nil {
		now = time.Now
	}
	return &jsonlSource{name: name, kind: kind, file: f, sc: sc, skip: skipLines, now: now, decode: decode}, nil
}

func (s *jsonlSource) Name() string     { return s.name }
func (s *jsonlSource) Kind() Kind       { return s.kind }
func (s *jsonlSource) LinesRead() int64 { return s.lineNo }
func (s *jsonlSource) Close() error     { return s.file.Close() }

func (s *jsonlSource) Next() (Record, bool, error) {
	for s.sc.Scan() {
		s.lineNo++
		if s.lineNo <= s.skip {
			continue
		}
		line := bytes.TrimSpace(s.sc.Bytes())
		if len(line) == 0 {
			continue
		}
		rec, err := s.decode(line, s.lineNo, s.now())
		if err != nil {
			return Record{}, false, fmt.Errorf("mechanismpanel: %s line %d: %w", s.name, s.lineNo, err)
		}
		return rec, true, nil
	}
	if err := s.sc.Err(); err != nil {
		return Record{}, false, err
	}
	return Record{}, false, nil
}

func envelope(source, stream, instrument string, seq int64, eventTime, receiveTime, processTime time.Time, payload any) (datatruth.EventEnvelope, error) {
	return datatruth.NewEnvelope(source, stream, instrument, seq, eventTime, receiveTime, processTime, payload)
}

// parseExchTsMillis 解析强平事件里"交易所毫秒时间戳(原样保留)"的字符串形式。
func parseExchTsMillis(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(ms).UTC(), true
}

// NewLiquidationSource 读取 OKX 强平事件流(驱动流)。
func NewLiquidationSource(path string, skipLines int64, now clockFunc) (Source, error) {
	return newJSONLSource("okx_liquidation", KindLiquidation, path, skipLines, now, func(raw []byte, seq int64, proc time.Time) (Record, error) {
		var rec LiquidationRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return Record{}, err
		}
		eventTime := rec.RecordedAt
		if t, ok := parseExchTsMillis(rec.ExchTs); ok {
			eventTime = t
		}
		env, err := envelope("okx", "fast_liquidation_events", rec.Symbol, seq, eventTime, rec.RecordedAt, proc, rec)
		if err != nil {
			return Record{}, err
		}
		return Record{Kind: KindLiquidation, Envelope: env, Liquidation: &rec}, nil
	})
}

// NewXVenueLiquidationSource 读取跨所强平事件流(确认流)。
func NewXVenueLiquidationSource(path string, skipLines int64, now clockFunc) (Source, error) {
	return newJSONLSource("xvenue_liquidation", KindXVenueLiquidation, path, skipLines, now, func(raw []byte, seq int64, proc time.Time) (Record, error) {
		var rec XVenueLiquidationRecord
		if env, ok, err := decodeCanonicalContext(raw, contextdata.KindXVenue, &rec); ok || err != nil {
			return Record{Kind: KindXVenueLiquidation, Envelope: env, XVenue: &rec}, err
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return Record{}, err
		}
		if strings.EqualFold(rec.Venue, "bybit") && strings.TrimSpace(rec.SideRaw) != "" {
			switch strings.ToUpper(strings.TrimSpace(rec.SideRaw)) {
			case "BUY":
				rec.PosSide = "long"
			case "SELL":
				rec.PosSide = "short"
			}
		}
		eventTime := rec.RecordedAt
		if rec.ExchTs > 0 {
			eventTime = time.UnixMilli(rec.ExchTs).UTC()
		}
		env, err := envelope(rec.Venue, "fast_xvenue_liq_events", rec.Symbol, seq, eventTime, rec.RecordedAt, proc, rec)
		if err != nil {
			return Record{}, err
		}
		return Record{Kind: KindXVenueLiquidation, Envelope: env, XVenue: &rec}, nil
	})
}

// NewMarketMidsSource 读取全市场 mid 标尺流(单行多币)。
func NewMarketMidsSource(path string, skipLines int64, now clockFunc) (Source, error) {
	return newJSONLSource("market_mids", KindMarketMids, path, skipLines, now, func(raw []byte, seq int64, proc time.Time) (Record, error) {
		var rec MarketMidsRecord
		if env, ok, err := decodeCanonicalContext(raw, contextdata.KindMids, &rec); ok || err != nil {
			return Record{Kind: KindMarketMids, Envelope: env, Mids: &rec}, err
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return Record{}, err
		}
		env, err := envelope("okx", "fast_market_mids", "", seq, rec.RecordedAt, rec.RecordedAt, proc, rec)
		if err != nil {
			return Record{}, err
		}
		return Record{Kind: KindMarketMids, Envelope: env, Mids: &rec}, nil
	})
}

// NewXMetaOISource 读取 xmeta OI/funding 快照流(单行多所多币)。
func NewXMetaOISource(path string, skipLines int64, now clockFunc) (Source, error) {
	return newJSONLSource("xmeta_oi", KindXMetaOI, path, skipLines, now, func(raw []byte, seq int64, proc time.Time) (Record, error) {
		var rec XMetaOIRecord
		if env, ok, err := decodeCanonicalContext(raw, contextdata.KindOI, &rec); ok || err != nil {
			return Record{Kind: KindXMetaOI, Envelope: env, OI: &rec}, err
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return Record{}, err
		}
		env, err := envelope("xmeta", "fast_xmeta_snapshots", "", seq, rec.RecordedAt, rec.RecordedAt, proc, rec)
		if err != nil {
			return Record{}, err
		}
		return Record{Kind: KindXMetaOI, Envelope: env, OI: &rec}, nil
	})
}

// NewDepthSource 读取盘口深度快照流。
func NewDepthSource(path string, skipLines int64, now clockFunc) (Source, error) {
	return newJSONLSource("depth", KindDepth, path, skipLines, now, func(raw []byte, seq int64, proc time.Time) (Record, error) {
		var rec DepthRecord
		if env, ok, err := decodeCanonicalContext(raw, contextdata.KindDepth, &rec); ok || err != nil {
			return Record{Kind: KindDepth, Envelope: env, Depth: &rec}, err
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return Record{}, err
		}
		env, err := envelope("okx", "depth", rec.Symbol, seq, rec.RecordedAt, rec.RecordedAt, proc, rec)
		if err != nil {
			return Record{}, err
		}
		return Record{Kind: KindDepth, Envelope: env, Depth: &rec}, nil
	})
}

func decodeCanonicalContext(raw []byte, want contextdata.Kind, payload any) (datatruth.EventEnvelope, bool, error) {
	var probe struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return datatruth.EventEnvelope{}, false, nil
	}
	if probe.SchemaVersion == "" {
		return datatruth.EventEnvelope{}, false, nil
	}
	if probe.SchemaVersion != contextdata.SchemaVersion {
		return datatruth.EventEnvelope{}, true, fmt.Errorf("mechanismpanel: unsupported context schema %q", probe.SchemaVersion)
	}
	var record contextdata.Record
	if err := json.Unmarshal(raw, &record); err != nil {
		return datatruth.EventEnvelope{}, true, err
	}
	if record.Kind != want {
		return datatruth.EventEnvelope{}, true, fmt.Errorf("mechanismpanel: context kind %q want %q", record.Kind, want)
	}
	if record.Event.EventTime.IsZero() || record.Event.ReceiveTime.IsZero() || record.Event.ProcessTime.IsZero() {
		return datatruth.EventEnvelope{}, true, fmt.Errorf("mechanismpanel: canonical context missing three clocks")
	}
	if err := json.Unmarshal(record.Event.Payload, payload); err != nil {
		return datatruth.EventEnvelope{}, true, fmt.Errorf("mechanismpanel: decode canonical %s payload: %w", want, err)
	}
	return record.Event, true, nil
}

// LoadInstrumentCatalog 加载 instrument ctVal 点在时目录(小文件,一次性载入,非流式)。
// 复用 internal/datatruth 已有的双时态 InstrumentCatalog/InstrumentVersion(ContractValue=ctVal)。
func LoadInstrumentCatalog(path string) (datatruth.InstrumentCatalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return datatruth.InstrumentCatalog{}, fmt.Errorf("mechanismpanel: open instrument catalog %s: %w", path, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return datatruth.InstrumentCatalog{}, err
	}
	var cat datatruth.InstrumentCatalog
	if err := json.Unmarshal(b, &cat); err != nil {
		return datatruth.InstrumentCatalog{}, fmt.Errorf("mechanismpanel: decode instrument catalog: %w", err)
	}
	if err := cat.Validate(); err != nil {
		return datatruth.InstrumentCatalog{}, err
	}
	if len(cat.Versions) == 0 {
		return datatruth.InstrumentCatalog{}, fmt.Errorf("mechanismpanel: instrument catalog has no PIT versions (expected {\"versions\":[...]} schema)")
	}
	return cat, nil
}

// CtValAt 在给定 event-time/known-at 下查询某 instrument 的合约面值(ctVal)。
// 现货(ContractValue 为空但 instrument 存在)按 1 处理;instrument 完全查不到则 found=false。
func CtValAt(cat datatruth.InstrumentCatalog, instrumentID string, eventTime, knownAt time.Time) (float64, bool) {
	v, ok := cat.InstrumentAt(instrumentID, eventTime, knownAt)
	if !ok {
		return 0, false
	}
	cv := strings.TrimSpace(v.ContractValue)
	if cv == "" {
		return 1, true
	}
	f, err := strconv.ParseFloat(cv, 64)
	if err != nil || f <= 0 {
		return 0, false
	}
	return f, true
}
