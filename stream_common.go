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

func startWebSocketPing(ctx context.Context, conn *websocket.Conn) func() {
	done := make(chan struct{})
	ticker := time.NewTicker(3 * time.Minute)
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
