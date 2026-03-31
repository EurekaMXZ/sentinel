package stream

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	redisDialTimeout = 5 * time.Second
	redisIOTimeout   = 5 * time.Second
	redisMaxIdle     = 8
	redisPubSubQueue = 256
)

type respError string

func (e respError) Error() string { return string(e) }

type respConn struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
}

func (c *respConn) close() { _ = c.conn.Close() }

// command writes one command and reads one reply. The caller owns the
// connection and must close it after an I/O error.
func (c *respConn) command(ctx context.Context, args ...string) (any, error) {
	deadline := time.Now().Add(redisIOTimeout)
	if value, ok := ctx.Deadline(); ok {
		deadline = value
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	if err := writeCommand(c.w, args); err != nil {
		return nil, err
	}
	if err := c.w.Flush(); err != nil {
		return nil, err
	}
	return readReply(c.r)
}

// respClient is a minimal RESP2 client. It keeps a small idle connection pool
// for commands; subscriptions use dedicated connections.
type respClient struct {
	addr     string
	password string
	db       int
	mu       sync.Mutex
	idle     []*respConn
	closed   bool
}

func newRESPClient(addr, password string, db int) *respClient {
	return &respClient{addr: addr, password: password, db: db}
}

func (c *respClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, conn := range c.idle {
		conn.close()
	}
	c.idle = nil
	return nil
}

func (c *respClient) get(ctx context.Context) (*respConn, error) {
	c.mu.Lock()
	if len(c.idle) > 0 {
		conn := c.idle[len(c.idle)-1]
		c.idle = c.idle[:len(c.idle)-1]
		c.mu.Unlock()
		return conn, nil
	}
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("redis client is closed")
	}
	c.mu.Unlock()
	dialer := net.Dialer{Timeout: redisDialTimeout}
	raw, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, err
	}
	conn := &respConn{conn: raw, r: bufio.NewReader(raw), w: bufio.NewWriter(raw)}
	if c.password != "" {
		reply, err := conn.command(ctx, "AUTH", c.password)
		if err != nil {
			conn.close()
			return nil, err
		}
		if redisErr, ok := reply.(respError); ok {
			conn.close()
			return nil, redisErr
		}
	}
	if c.db != 0 {
		reply, err := conn.command(ctx, "SELECT", strconv.Itoa(c.db))
		if err != nil {
			conn.close()
			return nil, err
		}
		if redisErr, ok := reply.(respError); ok {
			conn.close()
			return nil, redisErr
		}
	}
	return conn, nil
}

func (c *respClient) put(conn *respConn) {
	c.mu.Lock()
	if c.closed || len(c.idle) >= redisMaxIdle {
		c.mu.Unlock()
		conn.close()
		return
	}
	c.idle = append(c.idle, conn)
	c.mu.Unlock()
}

func (c *respClient) do(ctx context.Context, args ...string) (any, error) {
	conn, err := c.get(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := conn.command(ctx, args...)
	if err != nil {
		conn.close()
		return nil, err
	}
	c.put(conn)
	if redisErr, ok := reply.(respError); ok {
		return nil, redisErr
	}
	return reply, nil
}

func (c *respClient) string(ctx context.Context, args ...string) (string, error) {
	reply, err := c.do(ctx, args...)
	if err != nil {
		return "", err
	}
	value, ok := reply.(string)
	if !ok {
		return "", fmt.Errorf("unexpected Redis reply type %T", reply)
	}
	return value, nil
}

// subscribe opens a dedicated Pub/Sub connection. It waits for the subscribe
// acknowledgement so callers cannot miss messages published immediately after
// the call returns.
func (c *respClient) subscribe(ctx context.Context, channel string) (<-chan string, func(), error) {
	dialer := net.Dialer{Timeout: redisDialTimeout}
	raw, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, nil, err
	}
	conn := &respConn{conn: raw, r: bufio.NewReader(raw), w: bufio.NewWriter(raw)}
	if c.password != "" {
		if _, err := conn.command(ctx, "AUTH", c.password); err != nil {
			conn.close()
			return nil, nil, err
		}
	}
	if c.db != 0 {
		if _, err := conn.command(ctx, "SELECT", strconv.Itoa(c.db)); err != nil {
			conn.close()
			return nil, nil, err
		}
	}
	if err := raw.SetDeadline(time.Now().Add(redisIOTimeout)); err != nil {
		conn.close()
		return nil, nil, err
	}
	if err := writeCommand(conn.w, []string{"SUBSCRIBE", channel}); err != nil {
		conn.close()
		return nil, nil, err
	}
	if err := conn.w.Flush(); err != nil {
		conn.close()
		return nil, nil, err
	}
	ack, err := readReply(conn.r)
	if err != nil {
		conn.close()
		return nil, nil, err
	}
	if redisErr, ok := ack.(respError); ok {
		conn.close()
		return nil, nil, redisErr
	}
	if !isSubscribeAck(ack, channel) {
		conn.close()
		return nil, nil, errors.New("invalid Redis subscribe acknowledgement")
	}
	_ = raw.SetDeadline(time.Time{})

	messages := make(chan string, redisPubSubQueue)
	done := make(chan struct{})
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			close(done)
			conn.close()
		})
	}
	go func() {
		defer close(messages)
		for {
			reply, err := readReply(conn.r)
			if err != nil {
				return
			}
			fields, ok := reply.([]any)
			if !ok || len(fields) < 3 {
				continue
			}
			kind, _ := fields[0].(string)
			if kind != "message" {
				continue
			}
			payload, _ := fields[2].(string)
			select {
			case messages <- payload:
			case <-done:
				return
			}
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-done:
		}
	}()
	return messages, cancel, nil
}

func isSubscribeAck(reply any, channel string) bool {
	fields, ok := reply.([]any)
	if !ok || len(fields) < 3 {
		return false
	}
	kind, _ := fields[0].(string)
	name, _ := fields[1].(string)
	return kind == "subscribe" && name == channel
}

func writeCommand(w *bufio.Writer, args []string) error {
	if _, err := fmt.Fprintf(w, "*%d\r\n", len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if _, err := fmt.Fprintf(w, "$%d\r\n%s\r\n", len(arg), arg); err != nil {
			return err
		}
	}
	return nil
}

func readReply(r *bufio.Reader) (any, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 3 || line[len(line)-2] != '\r' {
		return nil, errors.New("invalid RESP reply line")
	}
	body := line[:len(line)-2]
	switch body[0] {
	case '+':
		return body[1:], nil
	case '-':
		return respError(body[1:]), nil
	case ':':
		value, err := strconv.ParseInt(body[1:], 10, 64)
		if err != nil {
			return nil, err
		}
		return value, nil
	case '$':
		size, err := strconv.Atoi(body[1:])
		if err != nil {
			return nil, err
		}
		if size < 0 {
			return nil, nil
		}
		data := make([]byte, size+2)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, err
		}
		if data[size] != '\r' || data[size+1] != '\n' {
			return nil, errors.New("invalid RESP bulk string")
		}
		return string(data[:size]), nil
	case '*':
		count, err := strconv.Atoi(body[1:])
		if err != nil {
			return nil, err
		}
		if count < 0 {
			return nil, nil
		}
		values := make([]any, count)
		for i := range values {
			values[i], err = readReply(r)
			if err != nil {
				return nil, err
			}
		}
		return values, nil
	default:
		return nil, fmt.Errorf("unsupported RESP reply %q", body)
	}
}
