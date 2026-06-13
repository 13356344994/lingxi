package app

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
	"lingxi/internal/venue/okx"
)

// deadManFalsePositiveVenue 模拟 binance:Capabilities 声明 HasNativeDeadMan=true,
// 但 ArmDeadMansSwitch 仍返回 ErrUnsupported(须按 symbol 逐笔武装,全局接口未实现)。
type deadManFalsePositiveVenue struct {
	fakeFundingVenue
}

func (d *deadManFalsePositiveVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{HasNativeDeadMan: true}
}

func (d *deadManFalsePositiveVenue) ArmDeadMansSwitch(context.Context, time.Duration) error {
	return venue.ErrUnsupported
}

// deadManArmFailVenue 声明支持 dead-man,但续期返回真实错误(非 unsupported)。
type deadManArmFailVenue struct {
	fakeFundingVenue
}

func (d *deadManArmFailVenue) Capabilities() venue.Capabilities {
	return venue.Capabilities{HasNativeDeadMan: true}
}

func (d *deadManArmFailVenue) ArmDeadMansSwitch(context.Context, time.Duration) error {
	return errors.New("network timeout")
}

func TestIsDeadManUnsupportedErr(t *testing.T) {
	if !isDeadManUnsupportedErr(venue.ErrUnsupported) {
		t.Fatal("ErrUnsupported should match")
	}
	if !isDeadManUnsupportedErr(errors.New("venue: capability unsupported")) {
		t.Fatal("capability unsupported message should match")
	}
	if isDeadManUnsupportedErr(errors.New("network timeout")) {
		t.Fatal("real errors must not match")
	}
}

func TestArmDeadMansForLegsSkipsUnsupportedVenue(t *testing.T) {
	var buf bytes.Buffer
	e := &CrossVenueExecutor{
		cfg: testCrossVenueExecutorConfig(),
		venues: map[string]venue.Venue{
			binance.VenueName: &deadManFalsePositiveVenue{fakeFundingVenue: fakeFundingVenue{name: binance.VenueName}},
			okx.VenueName:     newCrossVenueTestVenue(okx.VenueName, 0.001, 8, time.Now()),
		},
		logger:                   log.New(&buf, "", 0),
		deadManUnsupportedVenues: make(map[string]struct{}),
	}
	pos := CrossVenuePosition{HighVenue: binance.VenueName, LowVenue: okx.VenueName}

	e.armDeadMansForLegs(context.Background(), pos, true)
	e.armDeadMansForLegs(context.Background(), pos, true)

	if strings.Contains(buf.String(), "dead-man 续期失败") {
		t.Fatalf("unsupported venue should not log renewal failure, got: %q", buf.String())
	}
	e.mu.RLock()
	_, cached := e.deadManUnsupportedVenues[binance.VenueName]
	e.mu.RUnlock()
	if !cached {
		t.Fatal("binance should be cached as dead-man unsupported")
	}
}

func TestArmDeadMansForLegsLogsRealFailure(t *testing.T) {
	var buf bytes.Buffer
	e := &CrossVenueExecutor{
		cfg: testCrossVenueExecutorConfig(),
		venues: map[string]venue.Venue{
			okx.VenueName: &deadManArmFailVenue{fakeFundingVenue: fakeFundingVenue{name: okx.VenueName}},
		},
		logger:                   log.New(&buf, "", 0),
		deadManUnsupportedVenues: make(map[string]struct{}),
	}
	pos := CrossVenuePosition{HighVenue: okx.VenueName, LowVenue: okx.VenueName}

	e.armDeadMansForLegs(context.Background(), pos, true)

	if !strings.Contains(buf.String(), "dead-man 续期失败") {
		t.Fatalf("real renewal failure should be logged, got: %q", buf.String())
	}
}
