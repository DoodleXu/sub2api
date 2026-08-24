package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// ImageStorageBindingID returns a non-secret identity for the effective image
// object-store configuration. It is persisted with async tasks so a restarted
// worker cannot use a newly selected bucket/endpoint to read or delete objects
// written by an older configuration.
func ImageStorageBindingID(cfg *config.ImageStorageConfig) string {
	if cfg == nil {
		return ""
	}
	// Keep the fingerprint stable across equivalent config representations. The
	// settings resolver applies these defaults before constructing the S3 client,
	// while older tasks may have persisted a binding from config.yaml with empty
	// values. Without canonicalization, a restart or settings migration would
	// make those tasks look as if they belonged to a different bucket and hide
	// their durable result URLs/cleanup manifests.
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "auto"
	}
	prefix := strings.TrimSpace(cfg.Prefix)
	if prefix == "" {
		prefix = "images/"
	} else if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	material := struct {
		Endpoint       string `json:"endpoint"`
		Region         string `json:"region"`
		Bucket         string `json:"bucket"`
		Prefix         string `json:"prefix"`
		ForcePathStyle bool   `json:"force_path_style"`
	}{
		Endpoint:       strings.TrimSpace(cfg.Endpoint),
		Region:         region,
		Bucket:         strings.TrimSpace(cfg.Bucket),
		Prefix:         prefix,
		ForcePathStyle: cfg.ForcePathStyle,
	}
	// Credentials and public delivery URLs are intentionally excluded. They can
	// rotate without changing the object namespace; including them would hide
	// durable history and strand cleanup manifests for objects in the same
	// endpoint/region/bucket/prefix.
	payload, _ := json.Marshal(material)
	digest := sha256.Sum256(payload)
	return "imgbind_" + hex.EncodeToString(digest[:16])
}
