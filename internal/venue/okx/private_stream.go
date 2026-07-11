package okx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var ErrPrivateStreamAuthentication = errors.New("okx: 私有 WS 登录或订阅失败")

// Conn 是私有 WS 连接的最小契约,真实实现可用 gorilla/websocket(已是本仓库依赖,
// 参见 internal/app/fast_ws_collector.go 等公共行情 WS 收集器的用法)包一层实现本接口。
type Conn interface {
	WriteMessage(data []byte) error
	ReadMessage() ([]byte, error)
	Close() error
}

// Dialer 是建立 Conn 的最小契约,便于测试用假实现替换真实网络连接。
type Dialer interface {
	Dial(ctx context.Context, url string) (Conn, error)
}

// PrivateStreamConfig 汇总建立私有流所需的最小配置。
type PrivateStreamConfig struct {
	URL        string // 空则用 PrivateWSURL
	APIKey     string
	APISecret  string
	Passphrase string
	InstType   string // 订单频道过滤,空则 "ANY"
}

type gorillaPrivateDialer struct {
	dialer *websocket.Dialer
}

type gorillaPrivateConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func NewGorillaPrivateDialer() Dialer {
	d := *websocket.DefaultDialer
	return &gorillaPrivateDialer{dialer: &d}
}

func (d *gorillaPrivateDialer) Dial(ctx context.Context, url string) (Conn, error) {
	conn, _, err := d.dialer.DialContext(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	})
	return &gorillaPrivateConn{conn: conn}, nil
}

func (c *gorillaPrivateConn) WriteMessage(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

func (c *gorillaPrivateConn) ReadMessage() ([]byte, error) {
	_, data, err := c.conn.ReadMessage()
	if err == nil {
		_ = c.conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	}
	return data, err
}

func (c *gorillaPrivateConn) Close() error { return c.conn.Close() }

type bufferedPrivateConn struct {
	Conn
	pending [][]byte
}

func (c *bufferedPrivateConn) ReadMessage() ([]byte, error) {
	if len(c.pending) > 0 {
		msg := c.pending[0]
		c.pending = c.pending[1:]
		return msg, nil
	}
	return c.Conn.ReadMessage()
}

type privateControlEvent struct {
	Event string `json:"event"`
	Code  string `json:"code"`
	Msg   string `json:"msg"`
	Arg   struct {
		Channel string `json:"channel"`
	} `json:"arg"`
}

// DialLivePrivateStream 完成一次真实连接的登录与 orders/positions 订阅握手。
// 重连由 StreamUserData 外层负责；握手期间到达的数据帧会缓冲，避免确认帧与首批推送之间丢事件。
func DialLivePrivateStream(ctx context.Context, dialer Dialer, cfg PrivateStreamConfig) (Conn, error) {
	if dialer == nil {
		return nil, errors.New("okx: 私有 WS dialer 为空")
	}
	if strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.APISecret) == "" || strings.TrimSpace(cfg.Passphrase) == "" {
		return nil, errors.New("okx: 私有 WS 缺少 API key/secret/passphrase")
	}
	url := strings.TrimSpace(cfg.URL)
	if url == "" {
		url = PrivateWSURL
	}
	conn, err := dialer.Dial(ctx, url)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (Conn, error) {
		_ = conn.Close()
		return nil, err
	}
	login, err := BuildPrivateLoginFrame(cfg.APIKey, cfg.APISecret, cfg.Passphrase, time.Now().UTC())
	if err != nil {
		return fail(err)
	}
	if err := conn.WriteMessage(login); err != nil {
		return fail(err)
	}
	if err := awaitPrivateControl(ctx, conn, "login", ""); err != nil {
		return fail(err)
	}
	orders, _ := BuildOrdersSubscribeFrame(cfg.InstType)
	positions, _ := BuildPositionsSubscribeFrame(cfg.InstType)
	if err := conn.WriteMessage(orders); err != nil {
		return fail(err)
	}
	if err := conn.WriteMessage(positions); err != nil {
		return fail(err)
	}
	needed := map[string]bool{"orders": false, "positions": false}
	var pending [][]byte
	for !needed["orders"] || !needed["positions"] {
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		default:
		}
		raw, err := conn.ReadMessage()
		if err != nil {
			return fail(err)
		}
		if string(raw) == "pong" {
			continue
		}
		var control privateControlEvent
		if err := json.Unmarshal(raw, &control); err != nil || control.Event == "" {
			pending = append(pending, append([]byte(nil), raw...))
			continue
		}
		if control.Event == "error" || (control.Code != "" && control.Code != "0") {
			return fail(fmt.Errorf("%w: code=%s msg=%s", ErrPrivateStreamAuthentication, control.Code, control.Msg))
		}
		if control.Event == "subscribe" {
			if _, ok := needed[control.Arg.Channel]; ok {
				needed[control.Arg.Channel] = true
			}
		}
	}
	return &bufferedPrivateConn{Conn: conn, pending: pending}, nil
}

func awaitPrivateControl(ctx context.Context, conn Conn, event, channel string) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if string(raw) == "pong" {
			continue
		}
		var control privateControlEvent
		if err := json.Unmarshal(raw, &control); err != nil {
			continue
		}
		if control.Event == "error" || (control.Code != "" && control.Code != "0") {
			return fmt.Errorf("%w: code=%s msg=%s", ErrPrivateStreamAuthentication, control.Code, control.Msg)
		}
		if control.Event == event && (channel == "" || control.Arg.Channel == channel) {
			return nil
		}
	}
}
