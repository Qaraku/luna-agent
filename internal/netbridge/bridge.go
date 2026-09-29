// Package netbridge 提供单次命令专用的有界公共 TCP 转发协议。
// IPC 不提供文件、配置、进程或权限变更方法；沙箱中的客户端不能选择宿主拨号策略。
package netbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Qaraku/luna-agent/internal/netpolicy"
	"io"
	"net"
	"sync"
	"time"
)

const ChunkBytes = 16 * 1024
const MaxPacketBytes = 64 * 1024
const MaxConnections = 16

type DialFunc func(context.Context, string) (net.Conn, error)
type packet struct {
	ID       uint64 `json:"id"`
	Op       string `json:"op,omitempty"`
	Conn     uint64 `json:"conn,omitempty"`
	Target   string `json:"target,omitempty"`
	Data     []byte `json:"data,omitempty"`
	N        int    `json:"n,omitempty"`
	Deadline int64  `json:"deadline,omitempty"`
	Error    string `json:"error,omitempty"`
}
type socket struct {
	conn             net.Conn
	reading, writing bool
}
type host struct {
	ctx       context.Context
	wire      net.Conn
	dial      DialFunc
	mu        sync.Mutex
	sockets   map[uint64]*socket
	next      uint64
	opening   int
	responses chan packet
	work      sync.WaitGroup
}

func (h *host) reply(p packet) {
	if p.ID == 0 {
		return
	}
	select {
	case h.responses <- p:
	case <-h.ctx.Done():
	}
}
func (h *host) fail(p packet, message string) { h.reply(packet{ID: p.ID, Error: message}) }
func (h *host) closeSockets() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, s := range h.sockets {
		s.conn.Close()
		delete(h.sockets, id)
	}
}

// Serve 阻塞到 IPC 关闭或命令取消；返回前关闭全部外连并等待已启动的工作结束。
func Serve(parent context.Context, wire net.Conn, dial DialFunc) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if dial == nil {
		dial = netpolicy.DialPublic
	}
	h := &host{ctx: ctx, wire: wire, dial: dial, sockets: make(map[uint64]*socket), responses: make(chan packet, 64)}
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		encoder := json.NewEncoder(wire)
		for {
			select {
			case p := <-h.responses:
				if err := encoder.Encode(p); err != nil {
					cancel()
					wire.Close()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	watcherDone := make(chan struct{})
	go func() { defer close(watcherDone); <-ctx.Done(); wire.Close(); h.closeSockets() }()
	scanner := bufio.NewScanner(wire)
	scanner.Buffer(make([]byte, 4096), MaxPacketBytes)
	for scanner.Scan() {
		var p packet
		if json.Unmarshal(scanner.Bytes(), &p) != nil {
			break
		}
		h.dispatch(p)
	}
	cancel()
	wire.Close()
	<-watcherDone
	h.work.Wait()
	<-writerDone
	return scanner.Err()
}
func (h *host) dispatch(p packet) {
	if p.ID == 0 && p.Op != "close" {
		return
	}
	if p.Op == "open" {
		if len(p.Target) > 512 {
			h.fail(p, "target too long")
			return
		}
		h.mu.Lock()
		if len(h.sockets)+h.opening >= MaxConnections {
			h.mu.Unlock()
			h.fail(p, "network connection limit reached")
			return
		}
		h.opening++
		h.mu.Unlock()
		h.work.Add(1)
		go func() {
			defer h.work.Done()
			conn, err := h.dial(h.ctx, p.Target)
			h.mu.Lock()
			h.opening--
			if err == nil && conn == nil {
				err = fmt.Errorf("no connection")
			}
			if err == nil && h.ctx.Err() != nil {
				conn.Close()
				err = h.ctx.Err()
			}
			var id uint64
			if err == nil {
				h.next++
				id = h.next
				h.sockets[id] = &socket{conn: conn}
			}
			h.mu.Unlock()
			if err != nil {
				h.fail(p, err.Error())
				return
			}
			h.reply(packet{ID: p.ID, Conn: id})
		}()
		return
	}
	h.mu.Lock()
	s, ok := h.sockets[p.Conn]
	if !ok {
		h.mu.Unlock()
		h.fail(p, "connection is closed")
		return
	}
	switch p.Op {
	case "close":
		delete(h.sockets, p.Conn)
		h.mu.Unlock()
		s.conn.Close()
		h.reply(packet{ID: p.ID})
		return
	case "read":
		if p.N < 1 || p.N > ChunkBytes || s.reading {
			h.mu.Unlock()
			h.fail(p, "invalid or overlapping read")
			return
		}
		s.reading = true
	case "write":
		if len(p.Data) > ChunkBytes || s.writing {
			h.mu.Unlock()
			h.fail(p, "invalid or overlapping write")
			return
		}
		s.writing = true
	case "read-deadline", "write-deadline":
		h.mu.Unlock()
		deadline := time.Time{}
		if p.Deadline != 0 {
			deadline = time.Unix(0, p.Deadline)
		}
		if end, ok := h.ctx.Deadline(); ok && (deadline.IsZero() || end.Before(deadline)) {
			deadline = end
		}
		var err error
		if p.Op == "read-deadline" {
			err = s.conn.SetReadDeadline(deadline)
		} else {
			err = s.conn.SetWriteDeadline(deadline)
		}
		reply := packet{ID: p.ID}
		if err != nil {
			reply.Error = err.Error()
		}
		h.reply(reply)
		return
	default:
		h.mu.Unlock()
		h.fail(p, "unknown network operation")
		return
	}
	h.mu.Unlock()
	h.work.Add(1)
	go func() {
		defer h.work.Done()
		reply := packet{ID: p.ID}
		var err error
		if p.Op == "read" {
			reply.Data = make([]byte, p.N)
			reply.N, err = s.conn.Read(reply.Data)
			reply.Data = reply.Data[:reply.N]
		} else {
			reply.N, err = s.conn.Write(p.Data)
			if err == nil && reply.N != len(p.Data) {
				err = io.ErrShortWrite
			}
		}
		h.mu.Lock()
		if p.Op == "read" {
			s.reading = false
		} else {
			s.writing = false
		}
		h.mu.Unlock()
		if err != nil {
			reply.Error = err.Error()
		}
		h.reply(reply)
	}()
}
