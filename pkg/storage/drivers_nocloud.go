//go:build !cloudblob

package storage

// The default build does not register the Google Cloud Storage / Azure Blob gocloud
// drivers (they pull in the Google/Azure SDKs). Open returns a clear error for
// gs:// / azblob:// in this build; rebuild with -tags cloudblob to enable them.
const cloudBlobBuilt = false
