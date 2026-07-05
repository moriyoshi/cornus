//go:build cloudblob

package storage

// Building with -tags cloudblob registers the Google Cloud Storage and Azure Blob
// gocloud URL openers, so `gs://` and `azblob://` storage references work. These
// drivers pull in the Google Cloud and Azure SDKs, which is why they are behind a
// build tag rather than compiled into the default (lean, single-static-binary)
// build. See open.go for the clear error the default build returns instead.
import (
	_ "gocloud.dev/blob/azureblob"
	_ "gocloud.dev/blob/gcsblob"
)

// cloudBlobBuilt reports that the gs:// / azblob:// drivers are registered.
const cloudBlobBuilt = true
