package app

import (
	"context"
	"time"

	"github.com/gorilla/websocket"
)

const (
	binanceSpotUserWSBaseURL    = "wss://stream.binance.com:9443/ws/"
	binanceFuturesUserWSBaseURL = "wss://fstream.binance.com/private/ws/"
	binanceSpotCombinedWSURL    = "wss://stream.binance.com:9443/stream?streams="
	binanceFuturesCombinedWSURL = "wss://fstream.binance.com/public/stream?streams="
	// binanceSpotWSAPIBaseURL 是现货/杠杆账户的 WebSocket API 入口。
	// Binance 已于 2026-02-20 永久下线 listenKey 系列 REST 端点(POST/PUT/DELETE /api/v3/userDataStream,
	// 调用返回 410 Gone)，现货用户数据流必须改为在此连接上用 userDataStream.subscribe(.signature) 订阅。
	binanceSpotWSAPIBaseURL     = "wss://ws-api.binance.com:443/ws-api/v3"
	userStreamKeepaliveInterval = 30 * time.Minute
	// userStreamReadDeadline 是 WebSocket 读超时；每收到一个 pong 就续期。
	// userStreamPingInterval 必须【显著小于】读超时，否则空闲(无行情/无回报)时连接会在读超时处被反复掐断重连。
	// 早期 ping 间隔为 3 分钟、读超时 60 秒，导致静默期每 60 秒断一次。两者绑定为常量以防再次跑偏。
	userStreamReadDeadline = 60 * time.Second
	userStreamPingInterval = 20 * time.Second
)

func closeWebSocketOnContextDone(ctx context.Context, conn *websocket.Conn) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	return func() {
		select {
		case <-done:
		default:
			close(done)
		}
	}
}

func startWebSocketPing(ctx context.Context, conn *websocket.Conn) func() {
	done := make(chan struct{})
	ticker := time.NewTicker(userStreamPingInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				_ = conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second))
			}
		}
	}()
	return func() {
		select {
		case <-done:
		default:
			close(done)
		}
	}
}

func nextStreamBackoff(current time.Duration, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	return next
}
