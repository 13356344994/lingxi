package main

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
	userStreamKeepaliveInterval = 30 * time.Minute
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
