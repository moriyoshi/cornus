package dockerhost

// Engine methods for the libpod engine: lifecycle, images, introspection,
// archive, and volumes.
//
// Status codes below are MEASURED against Podman 5.8.2, not assumed, and they
// differ from Docker's often enough to matter: container create returns 201,
// start/stop return 204, volume remove returns 204 with 409 when the volume is
// in use, and image exists is a bodyless 204/404. Where a code is surprising the
// comment says so.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"cornus/pkg/api"
	"cornus/pkg/deploy"
	"cornus/pkg/hostenv"
)

// --- images ---------------------------------------------------------------

// imagePull pulls ref through libpod's native pull.
//
// tlsVerify is the reason this backend speaks libpod rather than compat. Cornus
// serves its own registry over plain HTTP on a loopback address; Docker trusts
// loopback registries implicitly, Podman does not — it consults
// /etc/containers/registries.conf, a file on the DAEMON host that cornus cannot
// write. libpod takes tlsVerify as a query parameter, so the decision travels
// with the request and no host configuration is required.
//
// Measured subtlety: the parameter is honored only when the key is PRESENT.
// Absent is not "true" — it leaves the containers/image default — so this always
// writes it explicitly.
func (e *podmanEngine) imagePull(ctx context.Context, ref string, credential *deploy.RegistryCredential) error {
	q := url.Values{}
	// Qualified here rather than at the call site: every ref that reaches podman
	// goes through one of these three methods, so normalizing at the engine
	// boundary keeps the 52 orchestration call sites — shared with the Docker
	// engine — free of any podman-specific reference rules.
	q.Set("reference", qualifyImageRef(ref))
	q.Set("tlsVerify", "false")
	// compatMode makes libpod emit Docker's jsonmessage progress instead of its
	// own {stream,error,images,id} shape, so the failure scan below is the same
	// one the Docker engine uses and there is one less format in the tree.
	q.Set("compatMode", "true")

	headers := http.Header{}
	if credential != nil {
		raw, err := json.Marshal(map[string]string{
			"username": credential.Username,
			"password": credential.Password,
		})
		if err != nil {
			return fmt.Errorf("encode registry credential: %w", err)
		}
		headers.Set("X-Registry-Auth", base64.URLEncoding.EncodeToString(raw))
	}

	resp, err := e.doWithHeaders(ctx, http.MethodPost, "/images/pull?"+q.Encode(), nil, headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusOK); err != nil {
		return err
	}
	// A FAILED pull still returns HTTP 200, reporting the failure inside the
	// stream — measured, and the same trap the Docker engine guards. Draining
	// without inspecting would treat a failed pull as success and let Apply tear
	// down a working deployment to replace it with nothing.
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if msg.Error != "" {
			return fmt.Errorf("%s", msg.Error)
		}
		if msg.ErrorDetail.Message != "" {
			return fmt.Errorf("%s", msg.ErrorDetail.Message)
		}
	}
}

// imageExists uses libpod's dedicated existence probe: 204 present, 404 absent.
// Cheaper than an inspect, and it has no Docker equivalent.
func (e *podmanEngine) imageExists(ctx context.Context, ref string) (bool, error) {
	resp, err := e.do(ctx, http.MethodGet, "/images/"+qualifyImageRef(ref)+"/exists", nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, expect(resp, http.StatusNoContent, http.StatusNotFound)
	}
}

// imageInspect returns the image's CONTENT id, which is what keeps the reuse
// fingerprint honest across a mutable tag.
func (e *podmanEngine) imageInspect(ctx context.Context, ref string) (string, bool, error) {
	resp, err := e.do(ctx, http.MethodGet, "/images/"+qualifyImageRef(ref)+"/json", nil)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			ID string `json:"Id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return "", false, err
		}
		return out.ID, true, nil
	case http.StatusNotFound:
		return "", false, nil
	default:
		return "", false, expect(resp, http.StatusOK)
	}
}

// --- container lifecycle ---------------------------------------------------

func (e *podmanEngine) containerStart(ctx context.Context, id string) error {
	resp, err := e.do(ctx, http.MethodPost, "/containers/"+id+"/start", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 204 is success; 304 means already running, which is not a failure for a
	// converge-to-desired-state caller.
	return expect(resp, http.StatusNoContent, http.StatusNotModified)
}

func (e *podmanEngine) containerStop(ctx context.Context, id string) error {
	resp, err := e.do(ctx, http.MethodPost, "/containers/"+id+"/stop", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return expect(resp, http.StatusNoContent, http.StatusNotModified)
}

func (e *podmanEngine) containerRestart(ctx context.Context, id string) error {
	resp, err := e.do(ctx, http.MethodPost, "/containers/"+id+"/restart", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return expect(resp, http.StatusNoContent)
}

// containerRemove force-removes the container and its anonymous volumes.
//
// v=true is `docker rm -v` parity, which the cross-backend contract requires:
// Delete must reap anonymous volumes, or a redeploy loop leaks one per replica
// per apply until the disk fills.
func (e *podmanEngine) containerRemove(ctx context.Context, id string) error {
	resp, err := e.do(ctx, http.MethodDelete, "/containers/"+id+"?force=true&v=true", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 404 is success for a delete-if-exists contract.
	return expect(resp, http.StatusOK, http.StatusNoContent, http.StatusNotFound)
}

// --- container introspection ----------------------------------------------

// containerList returns cornus-managed containers carrying label.
//
// The filter syntax is Docker's `label=value`, MEASURED rather than assumed: an
// earlier reading of podman's source suggested libpod clients split filters into
// ["key","value"] pairs, and against a live 5.8.2 that form matches nothing
// while `label=value` matches. The split form applies to the IMAGES endpoint,
// whose handler branches on whether the request is a libpod one.
func (e *podmanEngine) containerList(ctx context.Context, label string) ([]containerSummary, error) {
	filters, err := json.Marshal(map[string][]string{"label": {label}})
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("all", "true")
	q.Set("filters", string(filters))

	resp, err := e.do(ctx, http.MethodGet, "/containers/json?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusOK); err != nil {
		return nil, err
	}
	var out []containerSummary
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// --- archive (docker cp) ---------------------------------------------------
//
// All three are pass-throughs. Measured on 5.8.2: libpod serves the same
// X-Docker-Container-Path-Stat header (base64 JSON with name/size/mode/mtime/
// isDir/linkTarget) and the same tar format Docker does. This was the one
// Docker-format obligation with no in-tree fallback, since containerdhost's
// tarcopy packs from a local rootfs a socket-based engine does not have — so it
// being a pass-through is what makes this engine cheap.

func (e *podmanEngine) containerArchiveStat(ctx context.Context, id, path string) (api.PathStat, error) {
	q := url.Values{}
	q.Set("path", path)
	resp, err := e.do(ctx, http.MethodHead, "/containers/"+id+"/archive?"+q.Encode(), nil)
	if err != nil {
		return api.PathStat{}, err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusOK); err != nil {
		return api.PathStat{}, err
	}
	return decodePathStatHeader(resp.Header.Get("X-Docker-Container-Path-Stat"))
}

func (e *podmanEngine) containerArchiveGet(ctx context.Context, id, path string) (io.ReadCloser, api.PathStat, error) {
	q := url.Values{}
	q.Set("path", path)
	resp, err := e.do(ctx, http.MethodGet, "/containers/"+id+"/archive?"+q.Encode(), nil)
	if err != nil {
		return nil, api.PathStat{}, err
	}
	if err := expect(resp, http.StatusOK); err != nil {
		resp.Body.Close()
		return nil, api.PathStat{}, err
	}
	st, err := decodePathStatHeader(resp.Header.Get("X-Docker-Container-Path-Stat"))
	if err != nil {
		resp.Body.Close()
		return nil, api.PathStat{}, err
	}
	return resp.Body, st, nil
}

func (e *podmanEngine) containerArchivePut(ctx context.Context, id, path string, r io.Reader, opts api.CopyToOptions) error {
	q := url.Values{}
	q.Set("path", path)
	if opts.NoOverwriteDirNonDir {
		q.Set("noOverwriteDirNonDir", "true")
	}
	if opts.CopyUIDGID {
		q.Set("copyUIDGID", "true")
	}
	resp, err := e.doRaw(ctx, http.MethodPut, "/containers/"+id+"/archive?"+q.Encode(), "application/x-tar", r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return expect(resp, http.StatusOK)
}

// decodePathStatHeader decodes Docker's base64 path-stat header. libpod emits
// the identical header, so this is shared shape rather than translation.
func decodePathStatHeader(v string) (api.PathStat, error) {
	if v == "" {
		return api.PathStat{}, fmt.Errorf("podman: archive response carried no X-Docker-Container-Path-Stat header")
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return api.PathStat{}, fmt.Errorf("podman: decode path stat: %w", err)
	}
	var st struct {
		Name       string `json:"name"`
		Size       int64  `json:"size"`
		Mode       uint32 `json:"mode"`
		Mtime      string `json:"mtime"`
		LinkTarget string `json:"linkTarget"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return api.PathStat{}, fmt.Errorf("podman: decode path stat: %w", err)
	}
	return api.PathStat{
		Name:       st.Name,
		Size:       st.Size,
		Mode:       st.Mode,
		Mtime:      st.Mtime,
		LinkTarget: st.LinkTarget,
	}, nil
}

// --- volumes ---------------------------------------------------------------

// volumeEnsure creates the volume if it is absent.
//
// The request body uses Go FIELD NAMES, not lowercase json tags: libpod's
// VolumeCreateOptions is tagged for its query-string decoder (`schema:"..."`)
// and decoded here with encoding/json, which falls back to the field name. So it
// is "Options", not "opts" — an easy and silent thing to get wrong, since a
// mistyped key is simply ignored and the volume is created without its driver
// options.
func (e *podmanEngine) volumeEnsure(ctx context.Context, v api.VolumeSpec) error {
	body := map[string]any{"Name": v.Name}
	if v.Driver != "" {
		body["Driver"] = v.Driver
	}
	if len(v.DriverOpts) > 0 {
		body["Options"] = v.DriverOpts
	}
	if len(v.Labels) > 0 {
		body["Labels"] = v.Labels
	}
	// ignoreIfExists makes this idempotent server-side rather than racing an
	// exists-then-create.
	resp, err := e.do(ctx, http.MethodPost, "/volumes/create?ignoreIfExists=true", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return expect(resp, http.StatusCreated, http.StatusOK)
}

func (e *podmanEngine) volumeInspect(ctx context.Context, name string) (string, error) {
	resp, err := e.do(ctx, http.MethodGet, "/volumes/"+name+"/json", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusOK); err != nil {
		return "", err
	}
	var out struct {
		Mountpoint string `json:"Mountpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Mountpoint, nil
}

func (e *podmanEngine) volumeRemove(ctx context.Context, name string) error {
	resp, err := e.do(ctx, http.MethodDelete, "/volumes/"+name, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 404 is success (delete-if-exists). 409 means the volume is still in use,
	// which for the GC path is also not a failure worth failing a Delete over —
	// a volume another deployment still mounts must not be removed.
	if resp.StatusCode == http.StatusConflict {
		return nil
	}
	return expect(resp, http.StatusNoContent, http.StatusOK, http.StatusNotFound)
}

// --- host relationship -----------------------------------------------------

// selfInspect fetches the subset of a container inspect that hostenv needs to
// confirm this process's own identity on this runtime.
func (e *podmanEngine) selfInspect(ctx context.Context, id string) (hostenv.SelfInspect, error) {
	resp, err := e.do(ctx, http.MethodGet, "/containers/"+id+"/json", nil)
	if err != nil {
		return hostenv.SelfInspect{}, err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusOK); err != nil {
		return hostenv.SelfInspect{}, err
	}
	var out struct {
		Config struct {
			Hostname string `json:"Hostname"`
		} `json:"Config"`
		Mounts []struct {
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
		} `json:"Mounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return hostenv.SelfInspect{}, err
	}
	self := hostenv.SelfInspect{Hostname: out.Config.Hostname}
	for _, m := range out.Mounts {
		self.Mounts = append(self.Mounts, hostenv.MountPoint{
			Source:      m.Source,
			Destination: m.Destination,
		})
	}
	return self, nil
}

// rootlessInfo reports whether the daemon is running rootless, read from
// /libpod/info's host.security.rootless.
//
// Asked of the DAEMON rather than inferred from the socket path: a rootful
// socket can be bind-mounted anywhere, so the path proves nothing. It gates
// whether the host can route to a workload's container IP at all (see Phase 3).
func (e *podmanEngine) rootlessInfo(ctx context.Context) (bool, error) {
	resp, err := e.do(ctx, http.MethodGet, "/info", nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusOK); err != nil {
		return false, err
	}
	var out podmanInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.Host.Security.Rootless, nil
}

// podmanInfo is the subset of libpod's /info this backend reads.
//
// The id mappings are the interesting part and their shape is measured, not
// assumed — read off the rootless daemon in the E2E runner:
//
//	"idMappings":{"uidmap":[{"container_id":0,"host_id":1001,"size":1},
//	                        {"container_id":1,"host_id":100000,"size":65536}], "gidmap":[...]}
//
// Note the two ranges: container root maps to the podman USER, and everything
// above it to that user's subuid allocation. A workload running as uid 1000
// therefore needs host 100999 — not the range base, which is container root and
// just as unreadable to it.
//
// Rootful podman and Docker report null here, which decodes to no ranges and
// means the identity.
type podmanInfo struct {
	Host struct {
		Security struct {
			Rootless bool `json:"rootless"`
		} `json:"security"`
		IDMappings struct {
			UIDMap []podmanIDRange `json:"uidmap"`
			GIDMap []podmanIDRange `json:"gidmap"`
		} `json:"idMappings"`
	} `json:"host"`
}

type podmanIDRange struct {
	ContainerID int `json:"container_id"`
	HostID      int `json:"host_id"`
	Size        int `json:"size"`
}

// idMappings reports the daemon's id mapping in the neutral form.
func (e *podmanEngine) idMappings(ctx context.Context) (deploy.IDMap, error) {
	resp, err := e.do(ctx, http.MethodGet, "/info", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusOK); err != nil {
		return nil, err
	}
	var out podmanInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	var m deploy.IDMap
	for _, r := range out.Host.IDMappings.UIDMap {
		m = append(m, deploy.IDRange{ContainerID: r.ContainerID, HostID: r.HostID, Count: r.Size, UIDs: true})
	}
	for _, r := range out.Host.IDMappings.GIDMap {
		m = append(m, deploy.IDRange{ContainerID: r.ContainerID, HostID: r.HostID, Count: r.Size, GIDs: true})
	}
	return m, nil
}

// splitPodmanRef splits an image reference for endpoints that want name and tag
// apart. Shared shape with the Docker engine's splitRefTag; kept here so the
// libpod paths do not depend on that engine's helpers.
func splitPodmanRef(ref string) (name, tag string) {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		return ref[:at], ref[at+1:]
	}
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash {
		return ref[:colon], ref[colon+1:]
	}
	return ref, "latest"
}
