package artifacts

import (
	"fmt"
	"os"
	"strings"
)

// GarageArtifactStore is an ArtifactStore backed by a Garage S3-compatible
// cluster. Garage exposes a MinIO/S3-compatible API, so this is a thin
// constructor alias over MinIOArtifactStore pointing at a Garage endpoint.
//
// Environment variables consumed by NewGarageArtifactStoreFromEnv:
//
//	UBAG_GARAGE_ENDPOINT    – host:port of the Garage S3 API (required)
//	UBAG_GARAGE_ACCESS_KEY  – access key id (required)
//	UBAG_GARAGE_SECRET_KEY  – secret key (required)
//	UBAG_GARAGE_BUCKET      – bucket name (default ubag-artifacts)
//	UBAG_GARAGE_USE_SSL     – "true" to enable TLS (default false)
type GarageArtifactStore = MinIOArtifactStore // type alias, not a new type

// NewGarageArtifactStore constructs a GarageArtifactStore pointing at the
// given Garage S3-compatible endpoint. meta may be nil; a MemoryArtifactMeta
// will be used in that case.
func NewGarageArtifactStore(endpoint, accessKey, secretKey, bucket string, useSSL bool, meta ArtifactMeta) (*GarageArtifactStore, error) {
	return NewMinIOArtifactStore(endpoint, accessKey, secretKey, bucket, useSSL, meta)
}

// NewGarageArtifactStoreFromEnv constructs a GarageArtifactStore from
// environment variables. Returns an error if UBAG_GARAGE_ENDPOINT is unset.
func NewGarageArtifactStoreFromEnv(meta ArtifactMeta) (*GarageArtifactStore, error) {
	endpoint := strings.TrimSpace(os.Getenv("UBAG_GARAGE_ENDPOINT"))
	if endpoint == "" {
		return nil, fmt.Errorf("garage: UBAG_GARAGE_ENDPOINT is required")
	}
	accessKey := strings.TrimSpace(os.Getenv("UBAG_GARAGE_ACCESS_KEY"))
	secretKey := strings.TrimSpace(os.Getenv("UBAG_GARAGE_SECRET_KEY"))
	bucket := strings.TrimSpace(os.Getenv("UBAG_GARAGE_BUCKET"))
	if bucket == "" {
		bucket = defaultBucket
	}
	useSSL := strings.EqualFold(strings.TrimSpace(os.Getenv("UBAG_GARAGE_USE_SSL")), "true")
	return NewGarageArtifactStore(endpoint, accessKey, secretKey, bucket, useSSL, meta)
}
