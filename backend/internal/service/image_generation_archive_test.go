package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeArchivedImageAcceptsPaddedBase64(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "padded", raw: "aW1hZ2UtMQ=="},
		{name: "unpadded", raw: "aW1hZ2UtMQ"},
		{name: "data-url", raw: "data:image/png;base64,aW1hZ2UtMQ=="},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, mimeType, ext, err := decodeArchivedImage(ArchivedImageInput{B64JSON: tt.raw})

			require.NoError(t, err)
			require.Equal(t, []byte("image-1"), b)
			require.Equal(t, "image/png", mimeType)
			require.Equal(t, ".png", ext)
		})
	}
}
