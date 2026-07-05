package dockerhost

// Streaming Engine methods for the libpod engine: logs, stats, exec, attach.
//
// Three of the four are pass-throughs, MEASURED on Podman 5.8.2. libpod frames
// non-TTY logs, exec-start output and attach output with Docker's own 8-byte
// stdcopy header, byte-identically to the compat endpoint:
//
//	01 00 00 00 00 00 00 09  4f 55 54 2d 4c 49 4e 45   stdout, len 9, "OUT-LINE\n"
//	02 00 00 00 00 00 00 09  45 52 52 2d 4c 49 4e 45   stderr, len 9, "ERR-LINE\n"
//
// so deploy.Backend's framing contract is satisfied without wrapping anything.
//
// Stats is the exception, and the reason it is worth stating loudly: see
// containerStats.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"cornus/pkg/api"
)

// containerLogs streams the container's logs — a pass-through.
func (e *podmanEngine) containerLogs(ctx context.Context, id string, opts api.LogOptions) (io.ReadCloser, error) {
	q := url.Values{}
	q.Set("stdout", "true")
	q.Set("stderr", "true")
	if opts.Follow {
		q.Set("follow", "true")
	}
	if opts.Timestamps {
		q.Set("timestamps", "true")
	}
	tail := opts.Tail
	if tail == "" {
		tail = "all"
	}
	q.Set("tail", tail)
	if opts.Since != "" {
		q.Set("since", opts.Since)
	}
	if opts.Until != "" {
		q.Set("until", opts.Until)
	}
	resp, err := e.do(ctx, http.MethodGet, "/containers/"+id+"/logs?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if err := expect(resp, http.StatusOK); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}

// containerStats streams Docker-format metrics — and is the ONE place this
// engine must translate rather than forward.
//
// libpod's stats object is Docker's in every respect but two, both measured:
//
//   - the container id is keyed **"Id"**, where real Docker and cornus's own
//     hostrun.DockerStats use **"id"**. Forwarding verbatim would hand every
//     consumer a stats frame with a blank id — silently, since the field is
//     present-but-empty rather than missing.
//   - there is no "networks" key at all, so per-interface counters are simply
//     unavailable from this endpoint.
//
// Re-encoding through a decode/encode also normalizes anything else that drifts,
// at the cost of one JSON round-trip per sample — which is a sample per second
// at most, against a stream nobody is latency-sensitive about.
func (e *podmanEngine) containerStats(ctx context.Context, id string, stream bool) (io.ReadCloser, error) {
	q := url.Values{}
	q.Set("stream", strconv.FormatBool(stream))
	resp, err := e.do(ctx, http.MethodGet, "/containers/"+id+"/stats?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if err := expect(resp, http.StatusOK); err != nil {
		resp.Body.Close()
		return nil, err
	}

	pr, pw := io.Pipe()
	go func() {
		defer resp.Body.Close()
		dec := json.NewDecoder(resp.Body)
		enc := json.NewEncoder(pw)
		for {
			var raw map[string]json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				if err == io.EOF {
					pw.Close()
				} else {
					pw.CloseWithError(err)
				}
				return
			}
			normalizePodmanStats(raw)
			if err := enc.Encode(raw); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr, nil
}

// normalizePodmanStats rewrites libpod's stats object into Docker's shape,
// in place.
//
// Only the id key differs structurally, and it differs by CASE alone — which is
// exactly why this needs a test that reads the decoded value rather than one
// that checks bytes were written.
func normalizePodmanStats(raw map[string]json.RawMessage) {
	if _, ok := raw["id"]; ok {
		return // already Docker-shaped
	}
	if v, ok := raw["Id"]; ok {
		raw["id"] = v
		delete(raw, "Id")
	}
}

// --- exec ------------------------------------------------------------------

// execCreate creates an exec session.
//
// The request body is Docker-shaped PascalCase — measured: libpod's exec create
// takes {AttachStdout, AttachStderr, Cmd, Tty, ...} and answers {"Id": "..."},
// the same payload the Docker engine sends.
func (e *podmanEngine) execCreate(ctx context.Context, id string, cfg api.ExecConfig) (string, error) {
	body := execCreateBody{
		AttachStdin:  cfg.AttachStdin,
		AttachStdout: cfg.AttachStdout,
		AttachStderr: cfg.AttachStderr,
		Tty:          cfg.Tty,
		Cmd:          cfg.Cmd,
		Env:          cfg.Env,
		WorkingDir:   cfg.WorkingDir,
		User:         cfg.User,
		Privileged:   cfg.Privileged,
	}
	resp, err := e.do(ctx, http.MethodPost, "/containers/"+id+"/exec", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusCreated, http.StatusOK); err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("podman: exec create returned no id")
	}
	return out.ID, nil
}

// execStart runs a created exec and returns its raw stdio stream.
//
// Detach must be false: a detached start returns immediately and the caller
// bridges a stream carrying nothing, which reads as a command that produced no
// output rather than as an error.
func (e *podmanEngine) execStart(ctx context.Context, execID string, tty bool) (net.Conn, error) {
	body, err := json.Marshal(struct {
		Detach bool `json:"Detach"`
		Tty    bool `json:"Tty"`
	}{Detach: false, Tty: tty})
	if err != nil {
		return nil, err
	}
	return e.hijack(ctx, http.MethodPost, "/exec/"+execID+"/start", body)
}

func (e *podmanEngine) execInspect(ctx context.Context, execID string) (api.ExecState, error) {
	resp, err := e.do(ctx, http.MethodGet, "/exec/"+execID+"/json", nil)
	if err != nil {
		return api.ExecState{}, err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusOK); err != nil {
		return api.ExecState{}, err
	}
	var out struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return api.ExecState{}, err
	}
	return api.ExecState{Running: out.Running, ExitCode: out.ExitCode}, nil
}

func (e *podmanEngine) execResize(ctx context.Context, execID string, h, w uint) error {
	q := url.Values{}
	q.Set("h", strconv.FormatUint(uint64(h), 10))
	q.Set("w", strconv.FormatUint(uint64(w), 10))
	resp, err := e.do(ctx, http.MethodPost, "/exec/"+execID+"/resize?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return expect(resp, http.StatusOK, http.StatusCreated, http.StatusNoContent)
}

// containerAttach returns the container's raw stdio stream.
//
// Every flag is written explicitly as "1"/"0" rather than by presence, matching
// the Docker engine: omitting one takes the daemon's default instead of the
// caller's answer, and for stdin that is the difference between an interactive
// session and a silently read-only one.
func (e *podmanEngine) containerAttach(ctx context.Context, id string, cfg api.AttachConfig) (net.Conn, error) {
	q := url.Values{}
	setBool := func(k string, v bool) {
		if v {
			q.Set(k, "1")
		} else {
			q.Set(k, "0")
		}
	}
	setBool("stream", cfg.Stream)
	setBool("stdin", cfg.Stdin)
	setBool("stdout", cfg.Stdout)
	setBool("stderr", cfg.Stderr)
	setBool("logs", cfg.Logs)
	return e.hijack(ctx, http.MethodPost, "/containers/"+id+"/attach?"+q.Encode(), nil)
}
