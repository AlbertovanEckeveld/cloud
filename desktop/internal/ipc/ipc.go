// Package ipc connects the settings window to the background service over
// a Unix socket in the user's private config folder. Only processes of the
// same user may talk to it: the folder is 0700 and every connection's peer
// is checked with the kernel (SO_PEERCRED / LOCAL_PEERCRED).
package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"
)

// maxMessage bounds one request or answer.
const maxMessage = 1 << 20

// Request asks the service to do something.
type Request struct {
	Method string          `json:"method"`
	Args   json.RawMessage `json:"args,omitempty"`
}

// Response carries a result or an error message for the user.
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Handler answers requests.
type Handler func(ctx context.Context, method string, args json.RawMessage) (any, error)

// Listen serves h on the socket at path until ctx ends. A stale socket from
// a crashed service is replaced; a live one means another service runs.
func Listen(ctx context.Context, path string, h Handler, log *slog.Logger) (net.Listener, error) {
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, ErrRunning
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(ctx, c.(*net.UnixConn), h, log)
		}
	}()
	return ln, nil
}

// ErrRunning means a service is already listening.
var ErrRunning = errors.New("the Cloud service is already running")

func serve(ctx context.Context, c *net.UnixConn, h Handler, log *slog.Logger) {
	defer c.Close()
	if uid, err := peerUID(c); err != nil || uid != os.Getuid() {
		log.Warn("refused a connection from another user", "uid", uid, "error", err)
		return
	}
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), maxMessage)
	enc := json.NewEncoder(c)
	for sc.Scan() {
		var req Request
		var resp Response
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			resp.Error = "invalid request"
		} else if res, err := h(ctx, req.Method, req.Args); err != nil {
			resp.Error = err.Error()
		} else if resp.Result, err = json.Marshal(res); err != nil {
			resp.Error = "invalid result"
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

// Client calls the service.
type Client struct{ path string }

// NewClient returns a client for the socket at path.
func NewClient(path string) *Client { return &Client{path: path} }

// Running reports whether a service answers on the socket.
func (c *Client) Running() bool {
	conn, err := net.DialTimeout("unix", c.path, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Call runs one request; out receives the result (may be nil).
func (c *Client) Call(ctx context.Context, method string, args, out any) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.path)
	if err != nil {
		return errors.New("de Cloud-dienst draait niet")
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	req := Request{Method: method}
	if args != nil {
		if req.Args, err = json.Marshal(args); err != nil {
			return err
		}
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return err
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), maxMessage)
	if !sc.Scan() {
		return fmt.Errorf("no answer from the Cloud service: %w", sc.Err())
	}
	var resp Response
	if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if out != nil && resp.Result != nil {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}
