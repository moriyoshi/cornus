package dockerproxy

import (
	"context"
	"net/http"
	"strconv"
	"strings"
)

// Docker's two container-stream media types. A TTY container's stream is the
// terminal's raw byte stream; a non-TTY one carries stdout and stderr
// stdcopy-multiplexed into a single stream that the client has to demultiplex
// with stdcopy.StdCopy.
//
// Spelled out here rather than imported from github.com/docker/docker/api/types:
// that package would pull additional modules into the SHIPPED dependency set and
// therefore into THIRD_PARTY_NOTICES.md, which is a generated, byte-identity-gated
// file — a steep price for two string constants frozen by the wire protocol.
const (
	mediaTypeRawStream         = "application/vnd.docker.raw-stream"
	mediaTypeMultiplexedStream = "application/vnd.docker.multiplexed-stream"
)

// multiplexedStreamMinAPI is the API version that introduced the multiplexed
// media type. Before it, the daemon announced every stream as raw-stream and the
// client decided how to decode from the container's Config.Tty alone; from 1.42
// the Content-Type carries that answer, and modern clients read it.
const multiplexedStreamMinAPI = "1.42"

type ctxKey int

const apiVersionCtxKey ctxKey = iota

// withAPIVersion records the API version a client pinned in its request path, so
// handlers downstream of Handler's prefix stripping can still see it.
func withAPIVersion(r *http.Request, v string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), apiVersionCtxKey, v))
}

// requestAPIVersion reports the API version the client pinned as a /vX.Y path
// prefix. An UNVERSIONED request is a client declining to pin one, which the
// daemon serves at its own version — so it gets the newest behaviour, not the
// oldest. Docker's own CLI always pins, having negotiated down from /version.
func requestAPIVersion(r *http.Request) string {
	if v, ok := r.Context().Value(apiVersionCtxKey).(string); ok && v != "" {
		return v
	}
	return apiVersion
}

// apiVersionAtLeast reports whether the dotted major.minor version v is at least
// min. The components compare NUMERICALLY: "1.5" is OLDER than "1.42", which a
// lexicographic compare gets backwards — and 1.42 is precisely the boundary this
// is used for. An unparsable version reports false, keeping the pre-1.42
// raw-stream behaviour as the conservative fallback.
func apiVersionAtLeast(v, min string) bool {
	vMaj, vMin, ok := splitAPIVersion(v)
	if !ok {
		return false
	}
	mMaj, mMin, ok := splitAPIVersion(min)
	if !ok {
		return false
	}
	if vMaj != mMaj {
		return vMaj > mMaj
	}
	return vMin >= mMin
}

// splitAPIVersion parses a "1.43" / "v1.43" API version into its numeric parts.
func splitAPIVersion(v string) (major, minor int, ok bool) {
	maj, min, found := strings.Cut(strings.TrimPrefix(v, "v"), ".")
	if !found {
		return 0, 0, false
	}
	a, err := strconv.Atoi(maj)
	if err != nil {
		return 0, 0, false
	}
	b, err := strconv.Atoi(min)
	if err != nil {
		return 0, 0, false
	}
	return a, b, true
}

// streamContentType returns the Content-Type for a container's log/attach/exec
// stream, mirroring moby's rule (daemon/server/router/container/container_routes.go):
//
//	contentType := types.MediaTypeRawStream
//	if !tty && versions.GreaterThanOrEqualTo(version, "1.42") {
//	    contentType = types.MediaTypeMultiplexedStream
//	}
//
// This is not cosmetic. The proxy always emits stdcopy-framed bytes for a
// non-TTY container, so announcing raw-stream tells a client that trusts the
// media type to hand the frame headers to the user as text.
//
// The non-TTY answer is unambiguous: every backend frames, by the
// deploy.Backend contract. The TTY answer is only as good as the backend —
// dockerhost passes Docker's unframed TTY log bytes through (raw, matching this
// rule), while the kubernetes backend wraps them anyway (framed, contradicting
// it). That disagreement is a defect BELOW this layer, not something the proxy
// can resolve from the request, so this mirrors moby — the behaviour clients
// are written against — and the backend divergence is tracked separately.
func streamContentType(r *http.Request, tty bool) string {
	if !tty && apiVersionAtLeast(requestAPIVersion(r), multiplexedStreamMinAPI) {
		return mediaTypeMultiplexedStream
	}
	return mediaTypeRawStream
}
