package stream

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeEntry struct {
	id      string
	payload string
}

type fakeRedisServer struct {
	listener    net.Listener
	mu          sync.Mutex
	entries     []fakeEntry
	subscribers map[string][]net.Conn
	next        int64
}

func newFakeRedisServer(t *testing.T) *fakeRedisServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &fakeRedisServer{listener: listener, subscribers: make(map[string][]net.Conn)}
	go server.accept()
	t.Cleanup(func() {
		_ = listener.Close()
		server.mu.Lock()
		defer server.mu.Unlock()
		for _, conns := range server.subscribers {
			for _, conn := range conns {
				_ = conn.Close()
			}
		}
	})
	return server
}

func (s *fakeRedisServer) addr() string { return s.listener.Addr().String() }

func (s *fakeRedisServer) accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.serve(conn)
	}
}

func (s *fakeRedisServer) serve(conn net.Conn) {
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	for {
		args, err := readTestCommand(reader)
		if err != nil {
			_ = conn.Close()
			return
		}
		if len(args) == 0 {
			continue
		}
		switch strings.ToUpper(args[0]) {
		case "PING":
			writeTestSimple(writer, "PONG")
			_ = writer.Flush()
		case "AUTH", "SELECT":
			writeTestSimple(writer, "OK")
			_ = writer.Flush()
		case "XADD":
			if len(args) < 5 {
				writeTestError(writer, "ERR invalid XADD")
				_ = writer.Flush()
				continue
			}
			s.mu.Lock()
			s.next++
			id := fmt.Sprintf("1-%d", s.next)
			s.entries = append(s.entries, fakeEntry{id: id, payload: args[4]})
			s.mu.Unlock()
			writeTestBulk(writer, id)
			_ = writer.Flush()
		case "EXPIRE":
			writeTestInt(writer, 1)
			_ = writer.Flush()
		case "PUBLISH":
			if len(args) < 3 {
				writeTestError(writer, "ERR invalid PUBLISH")
				_ = writer.Flush()
				continue
			}
			s.mu.Lock()
			for _, subscriber := range s.subscribers[args[1]] {
				writeTestMessage(subscriber, args[1], args[2])
			}
			s.mu.Unlock()
			writeTestInt(writer, 1)
			_ = writer.Flush()
		case "XRANGE":
			s.writeRange(writer, args)
			_ = writer.Flush()
		case "SUBSCRIBE":
			if len(args) < 2 {
				writeTestError(writer, "ERR invalid SUBSCRIBE")
				_ = writer.Flush()
				continue
			}
			s.mu.Lock()
			s.subscribers[args[1]] = append(s.subscribers[args[1]], conn)
			s.mu.Unlock()
			writeTestSubscribeAck(writer, args[1])
			_ = writer.Flush()
			// Keep the connection open for pushed messages; do not read more
			// commands from this connection.
			return
		default:
			writeTestError(writer, "ERR unknown command")
			_ = writer.Flush()
		}
	}
}

func (s *fakeRedisServer) writeRange(writer *bufio.Writer, args []string) {
	if len(args) < 4 {
		writeTestError(writer, "ERR invalid XRANGE")
		return
	}
	start, end := args[2], args[3]
	count := 500
	if len(args) >= 6 && strings.EqualFold(args[4], "COUNT") {
		if value, err := strconv.Atoi(args[5]); err == nil {
			count = value
		}
	}
	s.mu.Lock()
	entries := make([]fakeEntry, 0, count)
	for _, entry := range s.entries {
		if start != "-" && CompareIDs(entry.id, strings.TrimPrefix(start, "(")) <= 0 && strings.HasPrefix(start, "(") {
			continue
		}
		if end != "+" && CompareIDs(entry.id, end) > 0 {
			continue
		}
		entries = append(entries, entry)
		if len(entries) >= count {
			break
		}
	}
	s.mu.Unlock()
	fmt.Fprintf(writer, "*%d\r\n", len(entries))
	for _, entry := range entries {
		fmt.Fprintf(writer, "*2\r\n")
		writeTestBulk(writer, entry.id)
		fmt.Fprintf(writer, "*2\r\n")
		writeTestBulk(writer, "event")
		writeTestBulk(writer, entry.payload)
	}
}

func readTestCommand(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) < 3 || line[0] != '*' || line[len(line)-2] != '\r' {
		return nil, fmt.Errorf("invalid command header %q", line)
	}
	count, err := strconv.Atoi(strings.TrimSuffix(line[1:], "\r\n"))
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		header, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if len(header) < 3 || header[0] != '$' || header[len(header)-2] != '\r' {
			return nil, fmt.Errorf("invalid bulk header %q", header)
		}
		size, err := strconv.Atoi(strings.TrimSuffix(header[1:], "\r\n"))
		if err != nil {
			return nil, err
		}
		data := make([]byte, size+2)
		if _, err := io.ReadFull(reader, data); err != nil {
			return nil, err
		}
		args = append(args, string(data[:size]))
	}
	return args, nil
}

func writeTestSimple(writer *bufio.Writer, value string) {
	fmt.Fprintf(writer, "+%s\r\n", value)
}

func writeTestError(writer *bufio.Writer, value string) {
	fmt.Fprintf(writer, "-%s\r\n", value)
}

func writeTestInt(writer *bufio.Writer, value int64) {
	fmt.Fprintf(writer, ":%d\r\n", value)
}

func writeTestBulk(writer *bufio.Writer, value string) {
	fmt.Fprintf(writer, "$%d\r\n%s\r\n", len(value), value)
}

func writeTestSubscribeAck(writer *bufio.Writer, channel string) {
	fmt.Fprintf(writer, "*3\r\n")
	writeTestBulk(writer, "subscribe")
	writeTestBulk(writer, channel)
	writeTestInt(writer, 1)
}

func writeTestMessage(conn net.Conn, channel, payload string) {
	writer := bufio.NewWriter(conn)
	fmt.Fprintf(writer, "*3\r\n")
	writeTestBulk(writer, "message")
	writeTestBulk(writer, channel)
	writeTestBulk(writer, payload)
	_ = writer.Flush()
}

func TestRedisPublishHistoryAndSubscribe(t *testing.T) {
	server := newFakeRedisServer(t)
	tenant := "0194e229-8576-7f42-8f08-3496913847cf"
	agent := "0194e229-8576-7f42-8f08-3496913847ce"
	redis, err := NewRedis(context.Background(), Config{Addr: server.addr(), TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer redis.Close()
	live, cancel, err := redis.Subscribe(context.Background(), tenant, agent)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	first, err := redis.Publish(context.Background(), tenant, agent, Event{Type: EventModelDelta,
		Data: []byte(`{"text":"hello"}`)})
	if err != nil || first == "" {
		t.Fatalf("first publish id=%q err=%v", first, err)
	}
	select {
	case message := <-live:
		if message.ID != first || message.Event.Type != EventModelDelta || string(message.Event.Data) != `{"text":"hello"}` {
			t.Fatalf("live message=%+v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live Pub/Sub message was not delivered")
	}
	second, err := redis.Publish(context.Background(), tenant, agent, Event{Type: EventAgentCompleted})
	if err != nil || second == "" || CompareIDs(second, first) <= 0 {
		t.Fatalf("second publish id=%q err=%v", second, err)
	}

	var replayed []Message
	last, err := redis.History(context.Background(), tenant, agent, "", func(message Message) error {
		replayed = append(replayed, message)
		return nil
	})
	if err != nil || len(replayed) != 2 || last != second || !replayed[1].Event.Terminal() {
		t.Fatalf("history=%+v last=%q err=%v", replayed, last, err)
	}
	replayed = nil
	if _, err := redis.History(context.Background(), tenant, agent, first, func(message Message) error {
		replayed = append(replayed, message)
		return nil
	}); err != nil || len(replayed) != 1 || replayed[0].ID != second {
		t.Fatalf("incremental history=%+v err=%v", replayed, err)
	}
	if !ValidID(first) || ValidID("bad") || CompareIDs("1-10", "1-9") <= 0 || CompareIDs("2-0", "1-99") <= 0 {
		t.Fatal("stream ID helpers are incorrect")
	}
}
