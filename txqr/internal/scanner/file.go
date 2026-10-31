package scanner

import (
	"os"
	"path/filepath"
)

// writeAtomic publishes a fully written file in the same directory. Failed
// writes leave the previous destination intact and remove the partial file.
func writeAtomic(path string, data []byte, overwrite bool) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".qrt-*.part")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Chmod(temp, 0644); err != nil {
		return err
	}
	if overwrite {
		return os.Rename(temp, path)
	}
	return publishExclusive(temp, path)
}
