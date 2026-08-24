package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestImageStorageBindingIDIsStableAndChangesWithStoreIdentity(t *testing.T) {
	base := &config.ImageStorageConfig{
		Endpoint:        "https://s3.example.test",
		Region:          "auto",
		Bucket:          "images",
		Prefix:          "images/",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret",
	}

	first := ImageStorageBindingID(base)
	require.NotEmpty(t, first)
	require.Equal(t, first, ImageStorageBindingID(base))

	changed := *base
	changed.Bucket = "other-images"
	require.NotEqual(t, first, ImageStorageBindingID(&changed))

	changed = *base
	changed.Prefix = "other-prefix/"
	require.NotEqual(t, first, ImageStorageBindingID(&changed))

	rotated := *base
	rotated.AccessKeyID = "rotated-access-key"
	rotated.SecretAccessKey = "rotated-secret"
	rotated.PublicBaseURL = "https://cdn.example.test/images"
	require.Equal(t, first, ImageStorageBindingID(&rotated), "credential and delivery URL rotation must preserve the object namespace binding")
}

func TestImageStorageBindingIDCanonicalizesResolverDefaults(t *testing.T) {
	legacy := &config.ImageStorageConfig{
		Bucket:          "images",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret",
	}
	normalized := &config.ImageStorageConfig{
		Region:          "auto",
		Bucket:          "images",
		Prefix:          "images/",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret",
	}
	require.Equal(t, ImageStorageBindingID(legacy), ImageStorageBindingID(normalized))

	rotatedCredentials := *normalized
	rotatedCredentials.AccessKeyID = "other-access-key"
	rotatedCredentials.SecretAccessKey = "other-secret"
	require.Equal(t, ImageStorageBindingID(normalized), ImageStorageBindingID(&rotatedCredentials))
}
