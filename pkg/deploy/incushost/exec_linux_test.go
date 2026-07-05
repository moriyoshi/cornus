//go:build linux

package incushost

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"
	incus "github.com/lxc/incus/v6/client"
	incusapi "github.com/lxc/incus/v6/shared/api"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
)

// rwc adapts a buffer to the io.ReadWriteCloser the exec stream API takes.
type rwc struct{ *bytes.Buffer }

func (rwc) Close() error { return nil }

// recordingControl is a stand-in for the exec's websocket control channel that
// records the messages the backend pushes to it.
type recordingControl struct {
	mu   sync.Mutex
	sent []incusapi.InstanceExecControl
	err  error
}

func (c *recordingControl) WriteJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if msg, ok := v.(incusapi.InstanceExecControl); ok {
		c.sent = append(c.sent, msg)
	}
	return c.err
}

func (c *recordingControl) messages() []incusapi.InstanceExecControl {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]incusapi.InstanceExecControl(nil), c.sent...)
}

// TestResizeMsgRendersIncusWindowResize pins the control message an interactive
// exec resize sends: Incus takes the terminal geometry as decimal strings under
// a "window-resize" command, so a shape change here silently stops resizing the
// remote PTY.
func TestResizeMsgRendersIncusWindowResize(t *testing.T) {
	msg := resizeMsg(120, 40)
	if msg.Command != "window-resize" {
		t.Fatalf("command = %q", msg.Command)
	}
	if msg.Args["width"] != "120" || msg.Args["height"] != "40" {
		t.Fatalf("args = %v, want width 120 / height 40", msg.Args)
	}
}

// TestExecReturnCodeReadsOperationMetadata pins how an exec's exit status is
// recovered. The value arrives as JSON, so it is a float64 in practice; the
// other numeric shapes are accepted defensively and anything else (or a missing
// key) must read as 0 rather than panicking on a type assertion.
func TestExecReturnCodeReadsOperationMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		meta map[string]any
		want int
	}{
		{"json float", map[string]any{"return": float64(7)}, 7},
		{"int", map[string]any{"return": 3}, 3},
		{"int64", map[string]any{"return": int64(9)}, 9},
		{"no metadata", nil, 0},
		{"no return key", map[string]any{"other": 1}, 0},
		{"unexpected type", map[string]any{"return": "boom"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := execReturnCode(&fakeOp{meta: tc.meta}); got != tc.want {
				t.Fatalf("execReturnCode = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestExecStartFramesNonTTYOutputAndRecordsTheExitCode pins the non-TTY exec
// contract: stdout and stderr come back stdcopy-multiplexed (so a caller can
// still tell them apart over one connection), and the process exit status from
// the finished operation is what ExecInspect subsequently reports.
func TestExecStartFramesNonTTYOutputAndRecordsTheExitCode(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")

	var gotInstance string
	var gotPost incusapi.InstanceExecPost
	f.execFn = func(name string, post incusapi.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
		gotInstance, gotPost = name, post
		_, _ = args.Stdout.Write([]byte("to stdout"))
		_, _ = args.Stderr.Write([]byte("to stderr"))
		close(args.DataDone)
		return &fakeOp{meta: map[string]any{"return": float64(7)}}, nil
	}

	id, err := b.ExecCreate(context.Background(), "web", api.ExecConfig{Cmd: []string{"sh", "-c", "echo hi"}})
	if err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	conn := rwc{new(bytes.Buffer)}
	if err := b.ExecStart(context.Background(), id, api.ExecStartConfig{}, conn); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}

	if gotInstance != "cornus-web-0" {
		t.Errorf("exec ran on %q", gotInstance)
	}
	if gotPost.Interactive || !gotPost.WaitForWS {
		t.Errorf("non-TTY exec post = %+v", gotPost)
	}
	var out, errb bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &errb, conn.Buffer); err != nil {
		t.Fatalf("StdCopy: %v", err)
	}
	if out.String() != "to stdout" || errb.String() != "to stderr" {
		t.Fatalf("demultiplexed streams = %q / %q", out.String(), errb.String())
	}

	st, err := b.ExecInspect(context.Background(), id)
	if err != nil {
		t.Fatalf("ExecInspect: %v", err)
	}
	if st.Running {
		t.Error("a finished exec must not report running")
	}
	if st.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7", st.ExitCode)
	}
}

// TestExecStartLeavesTTYOutputUnframed pins the other half of the framing
// contract: an interactive exec is a single raw stream (a terminal cannot carry
// stdcopy headers), and it is requested as interactive.
func TestExecStartLeavesTTYOutputUnframed(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")

	var interactive bool
	f.execFn = func(_ string, post incusapi.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
		interactive = post.Interactive
		_, _ = args.Stdout.Write([]byte("raw"))
		close(args.DataDone)
		return &fakeOp{}, nil
	}

	id, _ := b.ExecCreate(context.Background(), "web", api.ExecConfig{Cmd: []string{"sh"}, Tty: true})
	conn := rwc{new(bytes.Buffer)}
	if err := b.ExecStart(context.Background(), id, api.ExecStartConfig{}, conn); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	if !interactive {
		t.Error("a TTY exec must be requested as interactive")
	}
	if conn.String() != "raw" {
		t.Fatalf("TTY output = %q, want the unframed bytes", conn.String())
	}
}

// TestExecStartSeedsThePTYSizeFromAnEarlierResize pins the fix for the
// window-resize race: a client sends the terminal size right after create, which
// can land before the exec is started, so the size has to be carried into the
// exec request itself instead of being lost.
func TestExecStartSeedsThePTYSizeFromAnEarlierResize(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")

	var post incusapi.InstanceExecPost
	f.execFn = func(_ string, p incusapi.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
		post = p
		close(args.DataDone)
		return &fakeOp{}, nil
	}

	id, _ := b.ExecCreate(context.Background(), "web", api.ExecConfig{Cmd: []string{"sh"}, Tty: true})
	if err := b.ExecResize(context.Background(), id, 40, 120); err != nil {
		t.Fatalf("ExecResize: %v", err)
	}
	if err := b.ExecStart(context.Background(), id, api.ExecStartConfig{}, rwc{new(bytes.Buffer)}); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	if post.Width != 120 || post.Height != 40 {
		t.Fatalf("exec started with %dx%d, want 120x40", post.Width, post.Height)
	}
}

// TestExecStartWithoutAResizeLeavesTheSizeToIncus pins the negative case: with
// no size requested the exec post carries no geometry, so Incus applies its own
// default rather than a 0x0 terminal.
func TestExecStartWithoutAResizeLeavesTheSizeToIncus(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")

	var post incusapi.InstanceExecPost
	f.execFn = func(_ string, p incusapi.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
		post = p
		close(args.DataDone)
		return &fakeOp{}, nil
	}
	id, _ := b.ExecCreate(context.Background(), "web", api.ExecConfig{Cmd: []string{"sh"}, Tty: true})
	if err := b.ExecStart(context.Background(), id, api.ExecStartConfig{}, rwc{new(bytes.Buffer)}); err != nil {
		t.Fatalf("ExecStart: %v", err)
	}
	if post.Width != 0 || post.Height != 0 {
		t.Fatalf("unrequested geometry = %dx%d, want unset", post.Width, post.Height)
	}
}

// TestExecStartReportsDaemonAndOperationFailures pins that a failed exec is an
// error the caller sees, distinguishing the request failing from the operation
// failing, and that an unknown exec id never reaches the daemon at all.
func TestExecStartReportsDaemonAndOperationFailures(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")

	if err := b.ExecStart(context.Background(), "no-such-exec", api.ExecStartConfig{}, rwc{new(bytes.Buffer)}); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown exec id: got %v", err)
	}

	f.execFn = func(string, incusapi.InstanceExecPost, *incus.InstanceExecArgs) (incus.Operation, error) {
		return nil, errors.New("instance is not running")
	}
	id, _ := b.ExecCreate(context.Background(), "web", api.ExecConfig{Cmd: []string{"sh"}})
	err := b.ExecStart(context.Background(), id, api.ExecStartConfig{}, rwc{new(bytes.Buffer)})
	if err == nil || !strings.Contains(err.Error(), "instance is not running") {
		t.Fatalf("request failure: got %v", err)
	}

	f.execFn = func(_ string, _ incusapi.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
		return &fakeOp{waitErr: errors.New("command not found")}, nil
	}
	id2, _ := b.ExecCreate(context.Background(), "web", api.ExecConfig{Cmd: []string{"nope"}})
	err = b.ExecStart(context.Background(), id2, api.ExecStartConfig{}, rwc{new(bytes.Buffer)})
	if err == nil || !strings.Contains(err.Error(), "command not found") {
		t.Fatalf("operation failure: got %v", err)
	}
}

// TestAttachControlFlushesASizeRequestedBeforeTheChannelWasLive pins the other
// half of the resize race: a resize that arrived before the control websocket
// connected is replayed the moment it does, so the remote PTY ends up the size
// the client asked for.
func TestAttachControlFlushesASizeRequestedBeforeTheChannelWasLive(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")
	id, _ := b.ExecCreate(context.Background(), "web", api.ExecConfig{Cmd: []string{"sh"}, Tty: true})
	sess, ok := b.execs.get(id)
	if !ok {
		t.Fatal("session not registered")
	}

	if err := b.ExecResize(context.Background(), id, 24, 80); err != nil {
		t.Fatalf("ExecResize before the channel is live must not error: %v", err)
	}
	ctrl := &recordingControl{}
	sess.attachControl(ctrl)

	msgs := ctrl.messages()
	if len(msgs) != 1 || msgs[0].Command != "window-resize" || msgs[0].Args["width"] != "80" || msgs[0].Args["height"] != "24" {
		t.Fatalf("control messages on connect = %+v, want one 80x24 window-resize", msgs)
	}
}

// TestAttachControlSendsNothingWithoutAPendingSize pins that connecting the
// control channel does not push a bogus 0x0 resize when the client never asked
// for one.
func TestAttachControlSendsNothingWithoutAPendingSize(t *testing.T) {
	sess := &execSession{}
	ctrl := &recordingControl{}
	sess.attachControl(ctrl)
	if msgs := ctrl.messages(); len(msgs) != 0 {
		t.Fatalf("unexpected control messages: %+v", msgs)
	}
	if sess.control != wsControl(ctrl) {
		t.Fatal("the control channel must still be recorded for later resizes")
	}
}

// TestExecResizePushesToALiveControlChannel pins the steady-state resize path:
// once the control channel is up, a resize is written straight to it (and its
// failure is reported, not swallowed).
func TestExecResizePushesToALiveControlChannel(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")
	id, _ := b.ExecCreate(context.Background(), "web", api.ExecConfig{Cmd: []string{"sh"}, Tty: true})
	sess, _ := b.execs.get(id)

	ctrl := &recordingControl{}
	sess.attachControl(ctrl)
	if err := b.ExecResize(context.Background(), id, 50, 200); err != nil {
		t.Fatalf("ExecResize: %v", err)
	}
	msgs := ctrl.messages()
	if len(msgs) != 1 || msgs[0].Args["width"] != "200" || msgs[0].Args["height"] != "50" {
		t.Fatalf("control messages = %+v, want one 200x50 window-resize", msgs)
	}

	ctrl.err = errors.New("websocket closed")
	if err := b.ExecResize(context.Background(), id, 10, 10); err == nil {
		t.Fatal("a failed control write must be reported")
	}
}

// TestExecCreateWarnsForConfigIncusCannotHonor pins the exec-side refusal
// surface: privileged exec, a non-numeric user, and ssh-agent forwarding are all
// honored elsewhere, so this backend has to say it is ignoring them.
func TestExecCreateWarnsForConfigIncusCannotHonor(t *testing.T) {
	buf := captureLogs(t)
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")

	if _, err := b.ExecCreate(context.Background(), "web", api.ExecConfig{
		Cmd:          []string{"sh"},
		Privileged:   true,
		User:         "alice",
		ForwardAgent: true,
	}); err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	logs := buf.String()
	for _, want := range []string{
		"ignores exec Privileged",
		"accepts only a numeric uid",
		"does not support exec ssh-agent forwarding",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("missing warning %q in:\n%s", want, logs)
		}
	}
}

// TestExecCreateIsSilentForASupportedConfig is the negative half: a numeric uid
// IS honored (buildExecPost parses it), so it must not be warned about.
func TestExecCreateIsSilentForASupportedConfig(t *testing.T) {
	buf := captureLogs(t)
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")

	if _, err := b.ExecCreate(context.Background(), "web", api.ExecConfig{
		Cmd:  []string{"sh"},
		User: "1000",
		Env:  []string{"A=1"},
	}); err != nil {
		t.Fatalf("ExecCreate: %v", err)
	}
	if strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("a supported exec config should warn about nothing, got:\n%s", buf.String())
	}
}

// TestAttachIsRefusedEvenForALiveDeployment pins the deliberate refusal: Incus
// exposes a console, not docker-attach streams, so attach is an error rather
// than a half-working stream. The refusal still resolves the deployment first,
// so a missing name reports not-found rather than "unsupported".
func TestAttachIsRefusedEvenForALiveDeployment(t *testing.T) {
	f := newFakeConn()
	b := testBackend(f)
	applyOne(t, b, f, "web")
	err := b.Attach(context.Background(), "web", api.AttachConfig{}, rwc{new(bytes.Buffer)})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("attach on a live deployment: got %v", err)
	}
	if !strings.Contains(err.Error(), "use exec instead") {
		t.Errorf("the refusal should point at the alternative, got %q", err)
	}
	// An unknown deployment is not-found, not "unsupported".
	if err := b.Attach(context.Background(), "ghost", api.AttachConfig{}, nil); !errors.Is(err, deploy.ErrNotFound) {
		t.Fatalf("attach on a missing deployment: want ErrNotFound, got %v", err)
	}
}

// TestBackendDoesNotClaimMountingOrEgressSupport pins the capability surface the
// server type-asserts on before offering client-local mounts or enforced egress.
// This backend runs no caretaker companion, so it must NOT satisfy
// deploy.MountingBackend or deploy.EgressBackend: claiming either moves the
// failure from a clean API rejection into the running container (the same class
// of bug AgentForwardEnabled exists to prevent). If the companion ever lands
// here, this test is the one to delete — deliberately.
func TestBackendDoesNotClaimMountingOrEgressSupport(t *testing.T) {
	var b deploy.Backend = testBackend(newFakeConn())
	if _, ok := b.(deploy.MountingBackend); ok {
		t.Error("backend advertises MountingBackend but starts no companion to serve 9P mounts")
	}
	if _, ok := b.(deploy.EgressBackend); ok {
		t.Error("backend advertises EgressBackend but has no egress enforcement")
	}
	// The two capabilities it DOES declare stay declared.
	if _, ok := b.(deploy.RemoteCapable); !ok {
		t.Error("backend should still declare RemoteCapable")
	}
	if _, ok := b.(deploy.MetricsSampler); !ok {
		t.Error("backend should still declare MetricsSampler")
	}
}
