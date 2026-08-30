//go:build windows

package configwriter

import "golang.org/x/sys/windows"

func replaceFileAtomically(source, destination string) error {
	sourcePtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPtr, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	// MOVEFILE_REPLACE_EXISTING preserves replacement semantics without the
	// destructive remove-then-rename window. WRITE_THROUGH asks Windows to
	// flush the move before returning.
	return windows.MoveFileEx(sourcePtr, destinationPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
