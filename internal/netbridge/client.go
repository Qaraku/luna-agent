package netbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type Client struct {
	wire    net.Conn
	mu      sync.Mutex
	pending map[uint64]chan packet
	next    uint64
	send    chan packet
	done    chan struct{}
	once    sync.Once
}

func NewClient(wire net.Conn) *Client {
	c := &Client{wire: wire, pending: make(map[uint64]chan packet), send: make(chan packet, 64), done: make(chan struct{})}
	go func() {
		encoder := json.NewEncoder(wire)
		for {
			select {
			case p := <-c.send:
				if encoder.Encode(p) != nil {
					c.Close()
					return
				}
			case <-c.done:
				return
			}
		}
	}()
	go c.readLoop()
	return c
}
func (c *Client) readLoop() {
	defer c.Close()
	scanner := bufio.NewScanner(c.wire)
	scanner.Buffer(make([]byte, 4096), MaxPacketBytes)
	for scanner.Scan() {
		var p packet
		if json.Unmarshal(scanner.Bytes(), &p) != nil {
			return
		}
		c.mu.Lock()
		waiting := c.pending[p.ID]
		delete(c.pending, p.ID)
		if waiting != nil {
			waiting <- p
		}
		c.mu.Unlock()
		if waiting == nil && p.Conn != 0 {
			select {
			case c.send <- packet{Op: "close", Conn: p.Conn}:
			default:
				return
			}
		}
	}
}
func (c *Client) Close() error { c.once.Do(func() { close(c.done); c.wire.Close() }); return nil }
func (c *Client) call(ctx context.Context, p packet) (packet, error) {
	response := make(chan packet, 1)
	c.mu.Lock()
	c.next++
	p.ID = c.next
	c.pending[p.ID] = response
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, p.ID); c.mu.Unlock() }()
	select {
	case c.send <- p:
	case <-ctx.Done():
		return packet{}, ctx.Err()
	case <-c.done:
		return packet{}, net.ErrClosed
	}
	select {
	case reply := <-response:
		if reply.Error == "EOF" {
			return reply, io.EOF
		}
		if reply.Error != "" {
			return reply, errors.New(reply.Error)
		}
		return reply, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, p.ID)
		var orphan uint64
		select {
		case reply := <-response:
			orphan = reply.Conn
		default:
		}
		c.mu.Unlock()
		if orphan != 0 {
			select {
			case c.send <- packet{Op: "close", Conn: orphan}:
			default:
				c.Close()
			}
		}
		return packet{}, ctx.Err()
	case <-c.done:
		return packet{}, net.ErrClosed
	}
}
func (c *Client) DialContext(ctx context.Context, network, target string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("only public TCP proxy connections are supported")
	}
	reply, err := c.call(ctx, packet{Op: "open", Target: target})
	if err != nil {
		return nil, err
	}
	if reply.Conn == 0 {
		return nil, fmt.Errorf("invalid network connection identity")
	}
	conn := &remoteConn{client: c, id: reply.Conn, target: target}
	if err := ctx.Err(); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

type remoteConn struct {
	client          *Client
	id              uint64
	target          string
	closed          atomic.Bool
	readMu, writeMu sync.Mutex
}

func (c *remoteConn) Read(data []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	if len(data) == 0 {
		return 0, nil
	}
	n := len(data)
	if n > ChunkBytes {
		n = ChunkBytes
	}
	reply, err := c.client.call(context.Background(), packet{Op: "read", Conn: c.id, N: n})
	if len(reply.Data) > n {
		return 0, fmt.Errorf("invalid network read length")
	}
	return copy(data, reply.Data), err
}
func (c *remoteConn) Write(data []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	written := 0
	for len(data) > 0 {
		n := len(data)
		if n > ChunkBytes {
			n = ChunkBytes
		}
		reply, err := c.client.call(context.Background(), packet{Op: "write", Conn: c.id, Data: data[:n]})
		if reply.N < 0 || reply.N > n {
			return written, fmt.Errorf("invalid network write length")
		}
		written += reply.N
		if err != nil {
			return written, err
		}
		if reply.N != n {
			return written, io.ErrShortWrite
		}
		data = data[n:]
	}
	return written, nil
}
func (c *remoteConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.client.call(ctx, packet{Op: "close", Conn: c.id})
	return err
}

type address string

func (address) Network() string            { return "tcp" }
func (a address) String() string           { return string(a) }
func (c *remoteConn) LocalAddr() net.Addr  { return address("isolated-proxy") }
func (c *remoteConn) RemoteAddr() net.Addr { return address(c.target) }
func (c *remoteConn) deadline(op string, t time.Time) error {
	var value int64
	if !t.IsZero() {
		value = t.UnixNano()
	}
	_, err := c.client.call(context.Background(), packet{Op: op, Conn: c.id, Deadline: value})
	return err
}
func (c *remoteConn) SetReadDeadline(t time.Time) error  { return c.deadline("read-deadline", t) }
func (c *remoteConn) SetWriteDeadline(t time.Time) error { return c.deadline("write-deadline", t) }
func (c *remoteConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}
