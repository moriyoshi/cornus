package storage

import (
	"context"
	"encoding/json"
	"io"
	"strings"
)

// Descriptor is an OCI content descriptor as returned in a referrers index.
type Descriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	Size         int64             `json:"size"`
	ArtifactType string            `json:"artifactType,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
}

// referrerManifest is the subset of an image manifest / index needed to decide
// whether it refers to a subject and to describe it in a referrers response.
type referrerManifest struct {
	MediaType    string `json:"mediaType"`
	ArtifactType string `json:"artifactType"`
	Config       struct {
		MediaType string `json:"mediaType"`
	} `json:"config"`
	Subject     *gcDescriptor     `json:"subject"`
	Annotations map[string]string `json:"annotations"`
}

// Referrers returns descriptors for every manifest in repo whose subject field
// points at the given subject digest, per the OCI 1.1 referrers API. The subject
// itself need not exist.
func (b *Backend) Referrers(ctx context.Context, repo, subject string) ([]Descriptor, error) {
	if _, _, err := ParseDigest(subject); err != nil {
		return nil, err
	}
	hexes, err := b.manifestHexes(ctx, repo)
	if err != nil {
		return nil, err
	}
	var out []Descriptor
	for _, hexv := range hexes {
		digest := "sha256:" + hexv
		rc, gerr := b.GetBlob(ctx, digest)
		if gerr != nil {
			continue
		}
		data, rerr := io.ReadAll(io.LimitReader(rc, gcManifestReadLimit))
		rc.Close()
		if rerr != nil {
			continue
		}
		var m referrerManifest
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m.Subject == nil || m.Subject.Digest != subject {
			continue
		}
		mt := m.MediaType
		if mt == "" {
			mt = b.markerMediaType(ctx, repo, hexv)
		}
		// OCI fallback: when artifactType is unset, the config media type stands
		// in for it in the referrers descriptor.
		artifactType := m.ArtifactType
		if artifactType == "" {
			artifactType = m.Config.MediaType
		}
		out = append(out, Descriptor{
			MediaType:    mt,
			Digest:       digest,
			Size:         int64(len(data)),
			ArtifactType: artifactType,
			Annotations:  m.Annotations,
		})
	}
	return out, nil
}

// markerMediaType reads the stored media type marker for a manifest, used as the
// descriptor media type when the manifest body omits its own mediaType.
func (b *Backend) markerMediaType(ctx context.Context, repo, hexv string) string {
	const fallback = "application/vnd.oci.image.manifest.v1+json"
	rc, err := b.obj.Get(ctx, manifestKey(repo, hexv))
	if err != nil {
		return fallback
	}
	raw, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return fallback
	}
	if mt := strings.TrimSpace(string(raw)); mt != "" {
		return mt
	}
	return fallback
}
