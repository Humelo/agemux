//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pelletier/go-toml/v2"
)

// A shared Codex daemon owns many rollouts, so its file descriptors cannot
// identify which thread a particular TUI selected. Observe only that TUI's
// successful start/resume/fork RPC responses on a private Unix transport.
// Prompt/output traffic is preserved. Launch permission overrides use RPC metadata;
// the relay never injects prompts or terminal keys.
const codexIdentityPrefixLimit = 1024 * 1024

// RPC IDs are scoped to a WebSocket. The picker and chat connections can
// overlap and both begin at the same numeric ID.
type codexRequestKey struct {
	connection uint64
	id         string
}

type codexIdentityObserver struct {
	mu         sync.Mutex
	next       uint64
	applied    uint64
	pending    map[codexRequestKey]uint64
	thread     string
	connection uint64
}

func (o *codexIdentityObserver) request(data []byte) { o.requestOn(0, data) }

func (o *codexIdentityObserver) connect() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.connection++
	return o.connection
}

func (o *codexIdentityObserver) disconnect(connection uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for key := range o.pending {
		if key.connection == connection {
			delete(o.pending, key)
		}
	}
}

func (o *codexIdentityObserver) requestOn(connection uint64, data []byte) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(data, &req) != nil || len(req.ID) == 0 || string(req.ID) == "null" {
		return
	}
	if req.Method != "thread/start" && req.Method != "thread/resume" && req.Method != "thread/fork" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.pending == nil {
		o.pending = make(map[codexRequestKey]uint64)
	}
	// Bound an unresponsive client's metadata without touching its transport.
	if len(o.pending) >= 128 {
		clear(o.pending)
	}
	o.next++
	o.pending[codexRequestKey{connection, string(req.ID)}] = o.next
}

// Codex puts response id and thread identity before history. Read only that
// bounded prefix, including explicit null parentThreadId; never parse or retain
// full conversation history. An unsupported field order fails closed.
func codexResponseIdentity(prefix []byte) (requestID, threadID string) {
	d := json.NewDecoder(bytes.NewReader(prefix))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return "", ""
	}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return requestID, ""
		}
		switch token {
		case "id":
			var id json.RawMessage
			if d.Decode(&id) != nil {
				return "", ""
			}
			requestID = string(id)
		case "error":
			return requestID, ""
		case "result":
			if requestID == "" {
				return "", ""
			}
			if token, err := d.Token(); err != nil || token != json.Delim('{') {
				return requestID, ""
			}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return requestID, ""
				}
				if key != "thread" {
					var skip json.RawMessage
					if d.Decode(&skip) != nil {
						return requestID, ""
					}
					continue
				}
				if token, err := d.Token(); err != nil || token != json.Delim('{') {
					return requestID, ""
				}
				parentSeen := false
				for d.More() {
					key, err := d.Token()
					if err != nil {
						return requestID, ""
					}
					switch key {
					case "id":
						if d.Decode(&threadID) != nil || !validThreadID(threadID) {
							return requestID, ""
						}
					case "parentThreadId":
						var parent json.RawMessage
						if d.Decode(&parent) != nil || string(parent) != "null" {
							return requestID, ""
						}
						parentSeen = true
					default:
						var skip json.RawMessage
						if d.Decode(&skip) != nil {
							return requestID, ""
						}
					}
					if parentSeen && threadID != "" {
						return requestID, strings.ToLower(threadID)
					}
				}
				return requestID, ""
			}
			return requestID, ""
		default:
			var skip json.RawMessage
			if d.Decode(&skip) != nil {
				return requestID, ""
			}
		}
	}
	return requestID, ""
}

func (o *codexIdentityObserver) response(prefix []byte) { o.responseOn(0, prefix) }

func (o *codexIdentityObserver) responseOn(connection uint64, prefix []byte) {
	id, thread := codexResponseIdentity(prefix)
	if id == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	key := codexRequestKey{connection, id}
	sequence, ok := o.pending[key]
	delete(o.pending, key)
	if ok && thread != "" && sequence > o.applied {
		o.thread, o.applied = thread, sequence
	}
}

func (o *codexIdentityObserver) current() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.thread
}

type prefixCapture struct{ bytes.Buffer }

func (p *prefixCapture) Write(data []byte) (int, error) {
	if left := codexIdentityPrefixLimit - p.Len(); left > 0 {
		_, _ = p.Buffer.Write(data[:min(left, len(data))])
	}
	return len(data), nil
}

type codexDaemonBridge struct {
	observer    codexIdentityObserver
	endpoint    string
	dir         string
	server      *http.Server
	mu          sync.Mutex
	active      map[*websocket.Conn]bool
	closed      bool
	permissions []codexConfigAssignment
	fullAccess  bool
}

type codexConfigAssignment struct {
	path  []string
	value any
}

// Explicit --remote rejects permission CLI overrides on resume. Preserve those
// settings through the supported thread RPC fields instead. Other CLI options
// and prompt traffic are unchanged.
func (b *codexDaemonBridge) launchArgs(args []string) ([]string, error) {
	out := []string{args[0], "--remote", b.endpoint}
	b.permissions = nil
	b.fullAccess = false
	for i := 1; i < len(args); i++ {
		if args[i] == "--dangerously-bypass-approvals-and-sandbox" {
			b.fullAccess = true
			continue
		}
		if args[i] == "-c" && i+1 < len(args) {
			key, _, _ := strings.Cut(args[i+1], "=")
			key = strings.TrimSpace(key)
			if key == "approval_policy" || key == "approvals_reviewer" || key == "sandbox_mode" || key == "sandbox_workspace_write" || key == "permissions" || strings.HasPrefix(key, "sandbox_workspace_write.") || strings.HasPrefix(key, "permissions.") {
				_, rawValue, _ := strings.Cut(args[i+1], "=")
				rawValue = strings.TrimSpace(rawValue)
				var parsed map[string]any
				var value any = rawValue
				if toml.Unmarshal([]byte("value="+rawValue), &parsed) == nil {
					value = parsed["value"]
				}
				b.permissions = append(b.permissions, codexConfigAssignment{strings.Split(key, "."), value})
				i++
				continue
			}
		}
		out = append(out, args[i])
	}
	return out, nil
}

func assignCodexConfig(dst map[string]any, path []string, value any) {
	for _, key := range path[:len(path)-1] {
		child, _ := dst[key].(map[string]any)
		if child == nil {
			child = make(map[string]any)
			dst[key] = child
		}
		dst = child
	}
	dst[path[len(path)-1]] = value
}

func (b *codexDaemonBridge) permissionRequest(data []byte) ([]byte, error) {
	var req map[string]json.RawMessage
	if json.Unmarshal(data, &req) != nil {
		return data, nil
	}
	var method string
	_ = json.Unmarshal(req["method"], &method)
	if method != "thread/start" && method != "thread/resume" && method != "thread/fork" {
		return data, nil
	}
	if !b.fullAccess && len(b.permissions) == 0 {
		return data, nil
	}
	var params map[string]any
	if json.Unmarshal(req["params"], &params) != nil || params == nil {
		return nil, fmt.Errorf("invalid thread permission request")
	}
	if len(b.permissions) != 0 {
		config, _ := params["config"].(map[string]any)
		if config == nil {
			config = make(map[string]any)
		}
		for _, assignment := range b.permissions {
			// Clone table/array values for each request; a later dotted assignment
			// must not mutate the stored override or the next resume's settings.
			encoded, err := json.Marshal(assignment.value)
			if err != nil {
				return nil, err
			}
			var value any
			if err = json.Unmarshal(encoded, &value); err != nil {
				return nil, err
			}
			assignCodexConfig(config, assignment.path, value)
		}
		params["config"] = config
		for configKey, rpcKey := range map[string]string{"approval_policy": "approvalPolicy", "sandbox_mode": "sandbox", "approvals_reviewer": "approvalsReviewer"} {
			if value, ok := config[configKey]; ok {
				params[rpcKey] = value
			}
		}
	}
	if b.fullAccess {
		params["approvalPolicy"] = "never"
		params["sandbox"] = "danger-full-access"
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	req["params"] = encoded
	return json.Marshal(req)
}

func (b *codexDaemonBridge) stop() {
	b.mu.Lock()
	b.closed = true
	for conn := range b.active {
		_ = conn.Close()
	}
	b.mu.Unlock()
	_ = b.server.Close()
	_ = os.RemoveAll(b.dir)
}

func (b *codexDaemonBridge) register(conn *websocket.Conn) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		_ = conn.Close()
		return false
	}
	b.active[conn] = true
	return true
}

func (b *codexDaemonBridge) unregister(conn *websocket.Conn) {
	_ = conn.Close()
	b.mu.Lock()
	delete(b.active, conn)
	b.mu.Unlock()
}

func newCodexDaemonBridge(upstream string) (*codexDaemonBridge, error) {
	// Short path also fits Darwin's Unix-socket limit. MkdirTemp is mode0700;
	// the socket is mode0600 and is never exposed over TCP.
	dir, err := os.MkdirTemp("/tmp", "agx-cdx-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "rpc.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	b := &codexDaemonBridge{endpoint: "unix://" + path, dir: dir, active: make(map[*websocket.Conn]bool)}
	b.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 * 1024}
	b.server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			http.Error(w, "not available", http.StatusForbidden)
			return
		}
		b.mu.Lock()
		closed := b.closed
		b.mu.Unlock()
		if closed {
			http.Error(w, "relay is closed", http.StatusServiceUnavailable)
			return
		}
		// Codex's picker loader shuts down asynchronously. A resumed chat (and
		// in-session pickers) can connect while an older socket is still alive.
		// Keep independent upstream connections; never evict the current one.
		connection := b.observer.connect()
		defer b.observer.disconnect(connection)
		dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", upstream)
		}}
		remote, _, err := dialer.DialContext(r.Context(), "ws://localhost/", nil)
		if err != nil {
			http.Error(w, "Codex server unavailable", http.StatusBadGateway)
			return
		}
		if !b.register(remote) {
			return
		}
		defer b.unregister(remote)
		upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}
		client, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		if !b.register(client) {
			return
		}
		defer b.unregister(client)
		done := make(chan struct{}, 1)
		go func() {
			relayCodexRequests(remote, client, func(data []byte) { b.observer.requestOn(connection, data) }, b.permissionRequest)
			done <- struct{}{}
			_ = remote.Close()
		}()
		relayCodexRPC(client, remote, func(data []byte) { b.observer.responseOn(connection, data) })
		_ = client.Close()
		_ = remote.Close()
		<-done
	})
	go func() { _ = b.server.Serve(listener) }()
	return b, nil
}

func relayCodexRequests(dst, src *websocket.Conn, observe func([]byte), transform func([]byte) ([]byte, error)) {
	for {
		kind, reader, err := src.NextReader()
		if err != nil {
			return
		}
		prefix, err := io.ReadAll(io.LimitReader(reader, codexIdentityPrefixLimit+1))
		if err != nil {
			return
		}
		if kind == websocket.TextMessage && len(prefix) <= codexIdentityPrefixLimit {
			prefix, err = transform(prefix)
			if err != nil {
				return
			}
			observe(prefix)
		} else if kind == websocket.TextMessage {
			// Large prompt/image messages stream unchanged. Oversized or unknown
			// launch metadata must not silently lose permission settings.
			d := json.NewDecoder(bytes.NewReader(prefix))
			token, err := d.Token()
			if err != nil || token != json.Delim('{') {
				return
			}
			method := ""
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return
				}
				if key == "method" {
					if d.Decode(&method) != nil {
						return
					}
					break
				}
				var skip json.RawMessage
				if d.Decode(&skip) != nil {
					return
				}
			}
			if method == "" || method == "thread/start" || method == "thread/resume" || method == "thread/fork" {
				return
			}
		}
		writer, err := dst.NextWriter(kind)
		if err != nil {
			return
		}
		_, err = writer.Write(prefix)
		if err == nil {
			_, err = io.Copy(writer, reader)
		}
		closeErr := writer.Close()
		if err != nil || closeErr != nil {
			return
		}
	}
}

func relayCodexRPC(dst, src *websocket.Conn, observe func([]byte)) {
	for {
		kind, reader, err := src.NextReader()
		if err != nil {
			return
		}
		writer, err := dst.NextWriter(kind)
		if err != nil {
			return
		}
		var prefix prefixCapture
		_, copyErr := io.Copy(writer, io.TeeReader(reader, &prefix))
		// Register the completed request before flushing its final frame; the
		// upstream may respond immediately on the other goroutine.
		if copyErr == nil && kind == websocket.TextMessage {
			observe(prefix.Bytes())
		}
		if err := writer.Close(); err != nil || copyErr != nil {
			return
		}
	}
}

// Only use the daemon path when the installed CLI advertises daemon_auto_start.
// Older/custom CLIs retain the existing direct-TUI/FD discovery path. The start
// command is idempotent and never restarts or updates an already-running daemon.
func prepareCodexDaemonBridge(root string, configs []string) (*codexDaemonBridge, error) {
	if os.Getenv("AGEMUX_CODEX_DAEMON_TRACKING") == "0" {
		return nil, nil
	}
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var effective []string
		for _, config := range configs {
			effective = append(effective, "-c", config)
		}
		effective = append(effective, args...)
		cmd := exec.CommandContext(ctx, codexBin, effective...)
		cmd.Dir = root
		return cmd.Output()
	}
	features, err := run("features", "list")
	if err != nil {
		return nil, nil
	}
	enabled := false
	for _, line := range strings.Split(string(features), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "daemon_auto_start" && fields[len(fields)-1] == "true" {
			enabled = true
		}
	}
	if !enabled {
		return nil, nil
	}
	data, err := run("app-server", "daemon", "start")
	if err != nil {
		return nil, fmt.Errorf("start Codex background server: %w", err)
	}
	var status struct {
		Status     string `json:"status"`
		SocketPath string `json:"socketPath"`
	}
	if json.Unmarshal(data, &status) != nil || (status.Status != "alreadyRunning" && status.Status != "started") || !filepath.IsAbs(status.SocketPath) {
		return nil, fmt.Errorf("Codex background server returned no valid local socket")
	}
	info, err := os.Stat(status.SocketPath)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("Codex background server socket is unavailable")
	}
	return newCodexDaemonBridge(status.SocketPath)
}
