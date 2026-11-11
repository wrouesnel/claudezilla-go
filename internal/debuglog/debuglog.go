// Package debuglog appends to the host's debug log, created 0600 and rotated
// to "<path>.1" before it would exceed the size cap.
package debuglog

import (
	"os"
	"sync"
)

const DefaultMaxBytes = 10 * 1024 * 1024

var mu sync.Mutex

// Append writes msg to path, rotating first if needed.
func Append(path, msg string, maxBytes int64) error {
	mu.Lock()
	defer mu.Unlock()
	if st, err := os.Stat(path); err == nil && st.Size()+int64(len(msg)) > maxBytes {
		if err := os.Rename(path, path+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(msg)
	return err
}
