//go:build !windows

package configwriter

import "os"

func replaceFileAtomically(source, destination string) error {
	return os.Rename(source, destination)
}
