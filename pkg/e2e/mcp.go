package e2e

// MCP-over-stdio support for the Starlark harness. The harness acts as a real
// MCP client via the official SDK's CommandTransport, which starts
// cornus web --mcp-stdio and reserves the child's stdin/stdout for newline-
// delimited JSON-RPC. Initialization, repeated requests, tool errors, and
// graceful stdin-close shutdown therefore use the launch-a-command client path.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.starlark.net/starlark"
)

const defaultMCPRequestTimeout = 30 * time.Second

type mcpSession struct {
	client  *mcp.ClientSession
	stderr  lockedBuf
	timeout time.Duration
}

// bMCPStdio launches cornus web --mcp-stdio and completes the MCP initialize
// handshake. It returns an opaque handle plus the negotiated server identity.
func (h *Harness) bMCPStdio(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var host, composeFile, project, timeout string
	var envv starlark.Value
	if err := starlark.UnpackArgs("mcp_stdio", args, kwargs,
		"host?", &host, "compose_file?", &composeFile, "project?", &project,
		"timeout?", &timeout, "env?", &envv); err != nil {
		return nil, err
	}
	if host == "" {
		if h.registryHost == "" {
			return nil, fmt.Errorf("mcp_stdio: call serve() first (or pass host=)")
		}
		host = "http://" + h.registryHost
	}
	requestTimeout := defaultMCPRequestTimeout
	if timeout != "" {
		var err error
		requestTimeout, err = time.ParseDuration(timeout)
		if err != nil {
			return nil, fmt.Errorf("mcp_stdio: timeout: %w", err)
		}
		if requestTimeout <= 0 {
			return nil, fmt.Errorf("mcp_stdio: timeout must be positive")
		}
	}
	extraEnv, err := strMap(envv)
	if err != nil {
		return nil, fmt.Errorf("mcp_stdio: env: %w", err)
	}

	cmdArgs := []string{"web", "--mcp-stdio", "--host", host}
	if composeFile != "" {
		cmdArgs = append(cmdArgs, "-f", composeFile)
	}
	if project != "" {
		cmdArgs = append(cmdArgs, "-p", project)
	}
	cmd := exec.Command(h.cornusBin, cmdArgs...)
	cmd.Env = append(os.Environ(), h.target.ServeEnv()...)
	for k, v := range extraEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	sess := &mcpSession{timeout: requestTimeout}
	// stdout belongs exclusively to CommandTransport. Stderr remains diagnostic:
	// tee it live and retain it so a failed request or close explains why.
	cmd.Stderr = io.MultiWriter(h.out, &sess.stderr)

	client := mcp.NewClient(&mcp.Implementation{Name: "cornus-e2e", Version: "dev"}, nil)
	ctx, cancel := context.WithTimeout(h.ctx, requestTimeout)
	defer cancel()
	cs, err := client.Connect(ctx, &mcp.CommandTransport{
		Command:           cmd,
		TerminateDuration: 5 * time.Second,
	}, nil)
	if err != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil, fmt.Errorf("mcp_stdio: connect: %w%s", err, mcpStderrSuffix(&sess.stderr))
	}
	sess.client = cs
	if h.mcpSessions == nil {
		h.mcpSessions = map[string]*mcpSession{}
	}
	h.mcpSeq++
	handle := fmt.Sprintf("mcp-%d", h.mcpSeq)
	h.mcpSessions[handle] = sess

	init := cs.InitializeResult()
	result := map[string]any{"handle": handle}
	if init != nil {
		result["protocol_version"] = init.ProtocolVersion
		if init.ServerInfo != nil {
			result["server_name"] = init.ServerInfo.Name
			result["server_version"] = init.ServerInfo.Version
		}
	}
	h.logf("✓ MCP stdio session %s -> %s", handle, host)
	return anyDict(result), nil
}

func (h *Harness) bMCPListTools(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	handle, sess, err := h.unpackMCPSession("mcp_list_tools", args, kwargs)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(h.ctx, sess.timeout)
	defer cancel()
	res, err := sess.client.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp_list_tools %s: %w%s", handle, err, mcpStderrSuffix(&sess.stderr))
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return toStar(names), nil
}

func (h *Harness) bMCPCall(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var handle, tool string
	var arguments starlark.Value
	if err := starlark.UnpackArgs("mcp_call", args, kwargs,
		"handle", &handle, "tool", &tool, "arguments?", &arguments); err != nil {
		return nil, err
	}
	sess, err := h.getMCPSession("mcp_call", handle)
	if err != nil {
		return nil, err
	}
	decodedArgs := map[string]any{}
	if arguments != nil && arguments != starlark.None {
		v, err := fromStarlarkJSON(arguments)
		if err != nil {
			return nil, fmt.Errorf("mcp_call: arguments: %w", err)
		}
		var ok bool
		decodedArgs, ok = v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("mcp_call: arguments must be a dict")
		}
	}

	ctx, cancel := context.WithTimeout(h.ctx, sess.timeout)
	defer cancel()
	res, err := sess.client.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: decodedArgs})
	if err != nil {
		return nil, fmt.Errorf("mcp_call %s %s: %w%s", handle, tool, err, mcpStderrSuffix(&sess.stderr))
	}
	return mcpTextResult(res.IsError, mcpToolText(res)), nil
}

func (h *Harness) bMCPListResources(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	handle, sess, err := h.unpackMCPSession("mcp_list_resources", args, kwargs)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(h.ctx, sess.timeout)
	defer cancel()
	res, err := sess.client.ListResources(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp_list_resources %s: %w%s", handle, err, mcpStderrSuffix(&sess.stderr))
	}
	uris := make([]string, 0, len(res.Resources))
	for _, resource := range res.Resources {
		uris = append(uris, resource.URI)
	}
	sort.Strings(uris)
	return toStar(uris), nil
}

func (h *Harness) bMCPReadResource(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var handle, uri string
	if err := starlark.UnpackArgs("mcp_read_resource", args, kwargs,
		"handle", &handle, "uri", &uri); err != nil {
		return nil, err
	}
	sess, err := h.getMCPSession("mcp_read_resource", handle)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(h.ctx, sess.timeout)
	defer cancel()
	res, err := sess.client.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return nil, fmt.Errorf("mcp_read_resource %s %s: %w%s", handle, uri, err, mcpStderrSuffix(&sess.stderr))
	}
	var text strings.Builder
	for _, content := range res.Contents {
		if content.Text != "" {
			text.WriteString(content.Text)
		}
	}
	return mcpTextResult(false, text.String()), nil
}

func (h *Harness) bMCPClose(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var handle string
	if err := starlark.UnpackArgs("mcp_close", args, kwargs, "handle", &handle); err != nil {
		return nil, err
	}
	sess, err := h.getMCPSession("mcp_close", handle)
	if err != nil {
		return nil, err
	}
	// Delete first: Close is terminal even when the child exits non-zero, and a
	// teardown retry must not race the SDK's process Wait.
	delete(h.mcpSessions, handle)
	if err := sess.client.Close(); err != nil {
		return nil, fmt.Errorf("mcp_close %s: %w%s", handle, err, mcpStderrSuffix(&sess.stderr))
	}
	h.logf("✓ MCP stdio session %s closed cleanly", handle)
	return starlark.None, nil
}

func (h *Harness) unpackMCPSession(name string, args starlark.Tuple, kwargs []starlark.Tuple) (string, *mcpSession, error) {
	var handle string
	if err := starlark.UnpackArgs(name, args, kwargs, "handle", &handle); err != nil {
		return "", nil, err
	}
	sess, err := h.getMCPSession(name, handle)
	return handle, sess, err
}

func (h *Harness) getMCPSession(name, handle string) (*mcpSession, error) {
	if handle == "" {
		return nil, fmt.Errorf("%s: handle is required", name)
	}
	sess := h.mcpSessions[handle]
	if sess == nil {
		return nil, fmt.Errorf("%s: unknown or closed MCP handle %q", name, handle)
	}
	return sess, nil
}

func (h *Harness) stopMCPSessions() {
	if len(h.mcpSessions) == 0 {
		return
	}
	handles := make([]string, 0, len(h.mcpSessions))
	for handle := range h.mcpSessions {
		handles = append(handles, handle)
	}
	sort.Strings(handles)
	for _, handle := range handles {
		sess := h.mcpSessions[handle]
		delete(h.mcpSessions, handle)
		if err := sess.client.Close(); err != nil {
			h.logf("• warning: close MCP stdio session %s: %v%s", handle, err, mcpStderrSuffix(&sess.stderr))
		}
	}
}

func mcpToolText(res *mcp.CallToolResult) string {
	var text strings.Builder
	for _, content := range res.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return text.String()
}

func mcpTextResult(isError bool, text string) *starlark.Dict {
	return anyDict(map[string]any{
		"is_error": isError,
		"text":     text,
		"value":    decodeMCPJSON(text),
	})
}

func decodeMCPJSON(text string) any {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewBufferString(text))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil
	}
	return normalizeJSONNumbers(value)
}

func normalizeJSONNumbers(value any) any {
	switch v := value.(type) {
	case json.Number:
		if i, err := strconv.ParseInt(string(v), 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(string(v), 64); err == nil {
			return f
		}
		return string(v)
	case []any:
		for i := range v {
			v[i] = normalizeJSONNumbers(v[i])
		}
	case map[string]any:
		for k := range v {
			v[k] = normalizeJSONNumbers(v[k])
		}
	}
	return value
}

// fromStarlarkJSON recursively converts the JSON-shaped subset of Starlark.
// MCP tool arguments need this instead of anyMap/fromStar, whose intentionally
// scalar behavior is used by benchmark metadata.
func fromStarlarkJSON(value starlark.Value) (any, error) {
	switch v := value.(type) {
	case starlark.NoneType:
		return nil, nil
	case starlark.String:
		return string(v), nil
	case starlark.Bool:
		return bool(v), nil
	case starlark.Int:
		if i, ok := v.Int64(); ok {
			return i, nil
		}
		return nil, fmt.Errorf("integer %s is outside int64", v)
	case starlark.Float:
		return float64(v), nil
	case *starlark.List:
		out := make([]any, 0, v.Len())
		iter := v.Iterate()
		defer iter.Done()
		var item starlark.Value
		for iter.Next(&item) {
			decoded, err := fromStarlarkJSON(item)
			if err != nil {
				return nil, err
			}
			out = append(out, decoded)
		}
		return out, nil
	case starlark.Tuple:
		out := make([]any, 0, len(v))
		for _, item := range v {
			decoded, err := fromStarlarkJSON(item)
			if err != nil {
				return nil, err
			}
			out = append(out, decoded)
		}
		return out, nil
	case *starlark.Dict:
		out := make(map[string]any, v.Len())
		for _, item := range v.Items() {
			key, ok := starlark.AsString(item[0])
			if !ok {
				return nil, fmt.Errorf("dict keys must be strings, got %s", item[0].Type())
			}
			decoded, err := fromStarlarkJSON(item[1])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			out[key] = decoded
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported value type %s", value.Type())
	}
}

func mcpStderrSuffix(stderr *lockedBuf) string {
	out := strings.TrimSpace(stderr.String())
	if out == "" {
		return ""
	}
	return "\nstderr:\n" + out
}
