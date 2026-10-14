//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var daemonTestRoot = "019f0000" + "-0000-7000-8000-" + "000000000100"
var daemonTestNext = "019f0000" + "-0000-7000-8000-" + "000000000200"

func daemonReply(id int, thread, parent string) []byte {
	return []byte(fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":%q,"parentThreadId":%s,"turns":[]}}}`, id, thread, parent))
}

func TestCodexIdentityOnlyCorrelatedRootSuccess(t *testing.T) {
	var o codexIdentityObserver
	o.response(daemonReply(1, daemonTestRoot, "null"))
	if o.current() != "" {
		t.Fatal("uncorrelated response selected a thread")
	}
	o.request([]byte(`{"id":1,"method":"thread/read"}`))
	o.response(daemonReply(1, daemonTestRoot, "null"))
	if o.current() != "" {
		t.Fatal("metadata inspection selected a thread")
	}
	o.request([]byte(`{"id":2,"method":"thread/start"}`))
	o.response(daemonReply(2, daemonTestRoot, "null"))
	if o.current() != daemonTestRoot {
		t.Fatal("root start was not recorded")
	}
	o.request([]byte(`{"id":3,"method":"thread/resume"}`))
	o.response(daemonReply(3, daemonTestNext, fmt.Sprintf("%q", daemonTestRoot)))
	if o.current() != daemonTestRoot {
		t.Fatal("subagent replaced the root")
	}
	o.request([]byte(`{"id":4,"method":"thread/resume"}`))
	o.response([]byte(`{"id":4,"error":{"code":-1,"message":"failed"}}`))
	if o.current() != daemonTestRoot {
		t.Fatal("failed resume replaced the root")
	}
	o.request([]byte(`{"id":5,"method":"thread/fork"}`))
	o.response(daemonReply(5, daemonTestNext, "null"))
	if o.current() != daemonTestNext {
		t.Fatal("root fork was not recorded")
	}
}

func TestCodexIdentityOrderBoundsAndUnknownShape(t *testing.T) {
	var o codexIdentityObserver
	o.request([]byte(`{"id":1,"method":"thread/resume"}`))
	o.request([]byte(`{"id":2,"method":"thread/resume"}`))
	o.response(daemonReply(2, daemonTestNext, "null"))
	o.response(daemonReply(1, daemonTestRoot, "null"))
	if o.current() != daemonTestNext {
		t.Fatal("late older response replaced newer selection")
	}
	for _, raw := range []string{
		`{"id":3,"result":{"thread":{"id":"bad","parentThreadId":null}}}`,
		fmt.Sprintf(`{"id":3,"result":{"thread":{"id":%q}}}`, daemonTestRoot),
		fmt.Sprintf(`{"id":3,"result":{"thread":{"id":%q,"parentThreadId":""}}}`, daemonTestRoot),
		`{"id":3,"result":{"thread":null}}`,
	} {
		o.request([]byte(`{"id":3,"method":"thread/resume"}`))
		o.response([]byte(raw))
		if o.current() != daemonTestNext {
			t.Fatalf("malformed identity accepted: %s", raw)
		}
	}
	for i := 0; i < 300; i++ {
		o.request([]byte(fmt.Sprintf(`{"id":%d,"method":"thread/start"}`, i)))
	}
	if len(o.pending) > 128 {
		t.Fatal("unbounded pending IDs")
	}
	var capture prefixCapture
	_, _ = capture.Write(bytes.Repeat([]byte("x"), 2*codexIdentityPrefixLimit))
	if capture.Len() != codexIdentityPrefixLimit {
		t.Fatal("unbounded inspection prefix")
	}
}

func TestCodexResponseIdentityBeforeLargeHistory(t *testing.T) {
	prefix := []byte(fmt.Sprintf(`{"id":9,"result":{"thread":{"id":%q,"environments":[],"parentThreadId":null,"preview":"`, daemonTestRoot))
	prefix = append(prefix, bytes.Repeat([]byte("x"), codexIdentityPrefixLimit)...)
	id, thread := codexResponseIdentity(prefix[:codexIdentityPrefixLimit])
	if id != "9" || thread != daemonTestRoot {
		t.Fatal("history tail required to read leading identity")
	}
}

func TestCodexDaemonUnixRelayStreamsAndCleansUp(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "agx-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	upstream := filepath.Join(dir, "up.sock")
	ln, err := net.Listen("unix", upstream)
	if err != nil {
		t.Fatal(err)
	}
	errors := make(chan error, 1)
	payload := []byte(fmt.Sprintf(`{"id":7,"result":{"thread":{"id":%q,"parentThreadId":null,"preview":%q}}}`, daemonTestRoot, strings.Repeat("history", codexIdentityPrefixLimit)))
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errors <- err
			return
		}
		defer c.Close()
		_, raw, err := c.ReadMessage()
		if err != nil {
			errors <- err
			return
		}
		if string(raw) != `{"id":7,"method":"thread/resume"}` {
			errors <- fmt.Errorf("request changed")
			return
		}
		if err = c.WriteMessage(websocket.TextMessage, payload); err != nil {
			errors <- err
			return
		}
		_, raw, err = c.ReadMessage()
		if err != nil {
			errors <- err
			return
		}
		if string(raw) != "binary-data" {
			errors <- fmt.Errorf("binary changed")
			return
		}
		errors <- c.WriteMessage(websocket.BinaryMessage, raw)
	})}
	go server.Serve(ln)
	defer server.Close()
	proxy, err := newCodexDaemonBridge(upstream)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.stop()
	info, _ := os.Stat(proxy.dir)
	if info.Mode().Perm() != 0700 {
		t.Fatal("socket directory not private")
	}
	socketPath := strings.TrimPrefix(proxy.endpoint, "unix://")
	info, _ = os.Stat(socketPath)
	if info.Mode().Perm() != 0600 {
		t.Fatal("socket not private")
	}
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}
	client, _, err := dialer.Dial("ws://localhost/", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err = client.WriteMessage(websocket.TextMessage, []byte(`{"id":7,"method":"thread/resume"}`)); err != nil {
		t.Fatal(err)
	}
	_, got, err := client.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("large response was truncated or changed")
	}
	deadline := time.Now().Add(time.Second)
	for proxy.observer.current() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if proxy.observer.current() != daemonTestRoot {
		t.Fatal("resume identity missing")
	}
	if err = client.WriteMessage(websocket.BinaryMessage, []byte("binary-data")); err != nil {
		t.Fatal(err)
	}
	kind, got, err := client.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage || string(got) != "binary-data" {
		t.Fatal("binary relay failed", err)
	}
	if err = <-errors; err != nil {
		t.Fatal(err)
	}
	proxy.stop()
	if _, err = os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatal("socket not removed")
	}
}

func TestCodexDaemonProbeFallbackAndOptOut(t *testing.T) {
	old := codexBin
	defer func() { codexBin = old }()
	dir := t.TempDir()
	path := filepath.Join(dir, "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'daemon_auto_start stable false\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	codexBin = path
	b, err := prepareCodexDaemonBridge(dir, nil)
	if err != nil || b != nil {
		t.Fatal("disabled feature changed legacy launch")
	}
	t.Setenv("AGEMUX_CODEX_DAEMON_TRACKING", "0")
	codexBin = filepath.Join(dir, "must-not-execute")
	b, err = prepareCodexDaemonBridge(dir, nil)
	if err != nil || b != nil {
		t.Fatal("opt-out ignored")
	}
}

// Ensure the lightweight detector doesn't retain serialized conversation bodies.
func TestCodexObserverOnlyRetainsIdentity(t *testing.T) {
	var o codexIdentityObserver
	o.request([]byte(`{"id":"r","method":"thread/start","params":{"prompt":"private"}}`))
	data, _ := json.Marshal(map[string]any{"id": "r", "result": map[string]any{"thread": map[string]any{"id": daemonTestRoot, "parentThreadId": nil}}})
	o.response(data)
	if o.current() != daemonTestRoot || len(o.pending) != 0 {
		t.Fatal("identity not settled")
	}
}

func TestCodexDaemonProbeRespectsSessionConfig(t *testing.T) {
	old := codexBin
	defer func() { codexBin = old }()
	dir := t.TempDir()
	path := filepath.Join(dir, "codex")
	script := "#!/bin/sh\nif [ \"$1\" = -c ] && [ \"$2\" = features.daemon_auto_start=false ] && [ \"$3\" = features ]; then\n printf 'daemon_auto_start stable false\\n'\nelse\n exit 99\nfi\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	codexBin = path
	b, err := prepareCodexDaemonBridge(dir, []string{"features.daemon_auto_start=false"})
	if err != nil || b != nil {
		t.Fatal("session opt-out ignored", err)
	}
	t.Setenv("AGEMUX_CODEX_DAEMON_TRACKING", "0")
	command, _, err := runCommand("daemon-env-test", "codex-fresh", dir)
	if err != nil || !strings.Contains(command, "AGEMUX_CODEX_DAEMON_TRACKING=0") {
		t.Fatal("opt-out missing from shpool launch", command, err)
	}
}

func TestCodexRelayDisconnectReleasesBackpressuredConnection(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "agx-block-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	upstream := filepath.Join(dir, "up.sock")
	ln, err := net.Listen("unix", upstream)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan *websocket.Conn, 1)
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		ready <- c
		<-release // Deliberately do not read the client's large request.
	})}
	go server.Serve(ln)
	defer server.Close()
	defer close(release)
	proxy, err := newCodexDaemonBridge(upstream)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.stop()
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", strings.TrimPrefix(proxy.endpoint, "unix://"))
	}}
	client, _, err := dialer.Dial("ws://localhost/", nil)
	if err != nil {
		t.Fatal(err)
	}
	remote := <-ready
	writeDone := make(chan struct{})
	go func() {
		_ = client.WriteMessage(websocket.BinaryMessage, bytes.Repeat([]byte("x"), 16*1024*1024))
		close(writeDone)
	}()
	time.Sleep(50 * time.Millisecond)
	_ = client.Close()
	_ = remote.SetWriteDeadline(time.Now().Add(time.Second))
	_ = remote.WriteMessage(websocket.TextMessage, []byte(`{"method":"fixture/ping"}`))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		proxy.mu.Lock()
		active := proxy.clientActive
		proxy.mu.Unlock()
		if !active {
			<-writeDone
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("closed client left upstream writer and reconnection gate stuck")
}

func TestCodexRemotePermissionsPreserveAuthorizedCLISettings(t *testing.T) {
	b := &codexDaemonBridge{endpoint: "unix:///tmp/fixture.sock"}
	args, err := b.launchArgs([]string{"codex", "--dangerously-bypass-approvals-and-sandbox", "-C", "/tmp/work", "-m", "test-model", "-c", "model_reasoning_effort=high", "resume"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--dangerously") || !strings.Contains(joined, "model_reasoning_effort=high") || !strings.Contains(joined, "-m test-model") {
		t.Fatal(args)
	}
	for _, method := range []string{"thread/start", "thread/resume", "thread/fork"} {
		input := []byte(fmt.Sprintf(`{"id":3,"method":%q,"params":{"threadId":%q,"model":"test-model"}}`, method, daemonTestRoot))
		out, err := b.permissionRequest(input)
		if err != nil {
			t.Fatal(err)
		}
		var req struct {
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(out, &req) != nil {
			t.Fatal("invalid RPC")
		}
		if req.Params["approvalPolicy"] != "never" || req.Params["sandbox"] != "danger-full-access" || req.Params["model"] != "test-model" || req.Params["threadId"] != daemonTestRoot {
			t.Fatal(req.Params)
		}
	}
	input := []byte(`{"id":8,"method":"turn/start","params":{"input":[{"type":"text","text":"literal prompt"}]}}`)
	got, err := b.permissionRequest(input)
	if err != nil || !bytes.Equal(got, input) {
		t.Fatal("prompt was changed")
	}
	plain := &codexDaemonBridge{}
	_, _ = plain.launchArgs([]string{"codex", "resume"})
	got, err = plain.permissionRequest([]byte(`{"id":4,"method":"thread/resume","params":{}}`))
	if err != nil || strings.Contains(string(got), "danger-full-access") {
		t.Fatal("permission escalation without CLI authorization")
	}
}

func TestCodexRemoteRestrictedConfigAndWhitespace(t *testing.T) {
	b := &codexDaemonBridge{endpoint: "unix:///tmp/fixture.sock"}
	args, err := b.launchArgs([]string{"codex", "-c", `approval_policy="on-request"`, "-c", `sandbox_mode="workspace-write"`, "-c", `sandbox_workspace_write.network_access=false`, "-c", `sandbox_workspace_write.writable_roots=["/tmp/fixture"]`, "resume"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), "sandbox_") {
		t.Fatal(args)
	}
	out, err := b.permissionRequest([]byte(`{"id":"x","method":"thread/resume","params":{"config":{"other":"unchanged"},"sandbox":"danger-full-access"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Params map[string]any `json:"params"`
	}
	_ = json.Unmarshal(out, &req)
	if req.Params["sandbox"] != "workspace-write" || req.Params["approvalPolicy"] != "on-request" {
		t.Fatal(req.Params)
	}
	config := req.Params["config"].(map[string]any)
	limits := config["sandbox_workspace_write"].(map[string]any)
	if limits["network_access"] != false || len(limits["writable_roots"].([]any)) != 1 || config["other"] != "unchanged" {
		t.Fatal(config)
	}
}

func TestCodexPermissionAssignmentsReplaceTablesInOrder(t *testing.T) {
	for _, overrides := range [][]string{
		{`sandbox_workspace_write.network_access=true`, `sandbox_workspace_write={}`},
		{`sandbox_workspace_write={network_access=true}`, `sandbox_workspace_write={writable_roots=["/tmp/work"]}`},
	} {
		b := &codexDaemonBridge{}
		args := []string{"codex", "-c", "approval_policy = on-request", "-c", "permissions.custom.extends=:workspace"}
		for _, override := range overrides {
			args = append(args, "-c", override)
		}
		_, err := b.launchArgs(args)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			out, err := b.permissionRequest([]byte(`{"method":"thread/resume","params":{"config":{"sandbox_workspace_write":{"network_access":true,"old":true}}}}`))
			if err != nil {
				t.Fatal(err)
			}
			var req struct {
				Params map[string]any `json:"params"`
			}
			if json.Unmarshal(out, &req) != nil {
				t.Fatal("invalid RPC")
			}
			config := req.Params["config"].(map[string]any)
			limits := config["sandbox_workspace_write"].(map[string]any)
			if _, ok := limits["network_access"]; ok {
				t.Fatal("removed permission retained", limits)
			}
			if _, ok := limits["old"]; ok {
				t.Fatal("RPC table was merged instead of replaced", limits)
			}
			if req.Params["approvalPolicy"] != "on-request" {
				t.Fatal(req.Params)
			}
			if config["permissions"].(map[string]any)["custom"].(map[string]any)["extends"] != ":workspace" {
				t.Fatal(config)
			}
		}
	}
}
