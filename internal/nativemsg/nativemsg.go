// Package nativemsg implements Firefox's native messaging framing: a 4-byte
// little-endian length followed by that many bytes of UTF-8 JSON.
package nativemsg

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	// MaxInbound caps extension→host messages. The protocol allows 4 GB, but
	// no command comes near 10 MB; a cap stops a broken extension from forcing
	// a huge allocation.
	MaxInbound = 10 * 1024 * 1024
	// MaxOutbound is Firefox's limit for host→extension messages.
	MaxOutbound = 1024 * 1024
)

// Read returns the next message decoded, or io.EOF on a clean end of input.
func Read(r io.Reader) (map[string]any, error) {
	raw, err := ReadRaw(r)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ReadRaw returns the next message's JSON bytes, or io.EOF at a clean end.
func ReadRaw(r io.Reader) (json.RawMessage, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("unexpected EOF while reading header: %w", err)
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n == 0 {
		return json.RawMessage("{}"), nil
	}
	if n > MaxInbound {
		return nil, fmt.Errorf("message too large: %d bytes (max %d)", n, MaxInbound)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("unexpected EOF while reading message: %w", err)
	}
	return buf, nil
}

// Writer serializes framed messages to w; safe for concurrent use.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Write sends one message.
func (w *Writer) Write(msg any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(body) > MaxOutbound {
		return fmt.Errorf("message too large: %d bytes (max 1 MB)", len(body))
	}
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], uint32(len(body)))
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.w.Write(append(hdr[:], body...)); err != nil {
		return err
	}
	return nil
}
