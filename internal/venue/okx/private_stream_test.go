package okx

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type fakePrivateConn struct {
	mu     sync.Mutex
	reads  [][]byte
	writes [][]byte
	closed bool
}

func (c *fakePrivateConn) WriteMessage(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), data...))
	return nil
}

func (c *fakePrivateConn) ReadMessage() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reads) == 0 {
		return nil, errors.New("eof")
	}
	msg := c.reads[0]
	c.reads = c.reads[1:]
	return append([]byte(nil), msg...), nil
}

func (c *fakePrivateConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

type fakePrivateDialer struct {
	conn Conn
	err  error
}

func (d fakePrivateDialer) Dial(context.Context, string) (Conn, error) {
	return d.conn, d.err
}

func TestDialLivePrivateStreamAuthenticatesSubscribesAndBuffersEarlyData(t *testing.T) {
	early := []byte(`{"arg":{"channel":"orders"},"data":[{"instType":"SWAP","instId":"BTC-USDT-SWAP","state":"live"}]}`)
	conn := &fakePrivateConn{reads: [][]byte{
		[]byte(`{"event":"login","code":"0","msg":""}`),
		[]byte(`{"event":"subscribe","arg":{"channel":"orders"},"code":"0"}`),
		early,
		[]byte(`{"event":"subscribe","arg":{"channel":"positions"},"code":"0"}`),
	}}
	got, err := DialLivePrivateStream(context.Background(), fakePrivateDialer{conn: conn}, PrivateStreamConfig{
		APIKey: "k", APISecret: "0123456789abcdef0123456789abcdef", Passphrase: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(conn.writes) != 3 {
		t.Fatalf("应发送 login+2 subscribe, got %d", len(conn.writes))
	}
	raw, err := got.ReadMessage()
	if err != nil || string(raw) != string(early) {
		t.Fatalf("握手期间早到数据必须保留: raw=%s err=%v", raw, err)
	}
}

func TestDialLivePrivateStreamRejectsLoginFailure(t *testing.T) {
	conn := &fakePrivateConn{reads: [][]byte{
		[]byte(`{"event":"login","code":"60009","msg":"login failed"}`),
	}}
	_, err := DialLivePrivateStream(context.Background(), fakePrivateDialer{conn: conn}, PrivateStreamConfig{
		APIKey: "k", APISecret: "0123456789abcdef0123456789abcdef", Passphrase: "p",
	})
	if !errors.Is(err, ErrPrivateStreamAuthentication) {
		t.Fatalf("登录失败应 fail-closed, got %v", err)
	}
	if !conn.closed {
		t.Fatal("握手失败必须关闭连接")
	}
}
