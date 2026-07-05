package deploy

import (
	"context"
	"strconv"
	"strings"
)

// CredentialFile is one file delivery the SERVER rendered: the bytes a
// client-minted credential produced under the delivery's format, plus the mode
// and ownership they must land with inside the workload.
//
// The split of labour it implies is why file delivery needs no caretaker on a
// host backend. Producing the bytes requires the deploy-attach session, which
// only the server holds. Making them visible inside a workload requires a path
// the runtime resolves, which the server already has: the data-dir bind every
// containerized cornus is installed with, and the identity mapping when cornus
// runs on the host. Neither half needs a process alongside the workload.
type CredentialFile struct {
	Path    string // absolute path inside the container
	Content []byte // rendered per api.CredentialDelivery.Format
	Mode    int64  // unix permission bits; 0600
	UID     int    // owner inside the container — see CredentialFileOwner
	GID     int
}

// CredentialBinder is a backend that resolves host paths this server writes, so
// a file delivery can be realized as an ordinary read-only mount the server adds
// to the spec rather than by a caretaker inside the workload.
//
// It is a capability declaration, not an action: the mount rides in spec.Mounts
// and the backend realizes it like any other bind, so there is nothing to call.
// It exists because the question is not "is this a host backend" but "will this
// backend resolve a path this server can write", and those differ. A dockerhost
// in remote mode drives a daemon that may be on another machine entirely, where
// the server's path names nothing — and the runtime would bind an empty directory
// rather than fail, handing the workload a credential file that silently is not
// there. A backend must answer false in that configuration.
type CredentialBinder interface {
	Backend
	// BindsCredentialDir reports whether this backend, AS CURRENTLY CONFIGURED,
	// resolves paths this server writes. False sends file deliveries back to a
	// caretaker, or to a refusal on a backend that has none.
	BindsCredentialDir(ctx context.Context) bool
}

// CredentialFileOwner resolves the uid/gid a credential file must be owned by so
// the workload can actually read it, from the spec's `user:` field.
//
// A file the server writes lands owned by the server's own uid, and mode 0600
// then makes it unreadable by a container running as anyone else — surfacing as
// the application's own "permission denied" a long way from its cause. ok is
// false when the spec names a user this server cannot resolve to numbers: a NAME
// like "app" lives in the image's /etc/passwd, which cornus does not read. The
// caller warns rather than guessing, because guessing root is precisely the
// silently-unreadable case.
func CredentialFileOwner(user string) (uid, gid int, ok bool) {
	if user == "" {
		// The container runs as whatever the image's USER says, which is not
		// visible here. Root is right for the common case and wrong for an image
		// that sets USER — the residual gap this function cannot close.
		return 0, 0, true
	}
	name, group, _ := strings.Cut(user, ":")
	uid, err := strconv.Atoi(name)
	if err != nil {
		return 0, 0, false
	}
	if group == "" {
		// `user: 1000` means uid 1000 and its primary group, which cornus cannot
		// look up either; the uid is the closest defensible answer.
		return uid, uid, true
	}
	gid, err = strconv.Atoi(group)
	if err != nil {
		return 0, 0, false
	}
	return uid, gid, true
}
