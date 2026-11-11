package nativemsg

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

func TestWriteFraming(t *testing.T) {
	var b bytes.Buffer
	if err := NewWriter(&b).Write(map[string]any{"note": "♪ 日本語"}); err != nil {
		t.Fatal(err)
	}
	n := binary.LittleEndian.Uint32(b.Bytes()[:4])
	if int(n) != b.Len()-4 || !strings.Contains(b.String(), "♪ 日本語") {
		t.Fatalf("bad frame: len %d, body %q", n, b.Bytes()[4:])
	}
	m, err := Read(&b)
	if err != nil || m["note"] != "♪ 日本語" {
		t.Fatalf("round trip: %v %v", m, err)
	}
	if _, err := Read(&b); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestOutboundCap(t *testing.T) {
	// {"x":"..."} is 8 bytes of JSON around the string.
	at := strings.Repeat("a", MaxOutbound-8)
	if err := NewWriter(io.Discard).Write(map[string]string{"x": at}); err != nil {
		t.Fatal("1 MB exactly must pass:", err)
	}
	if err := NewWriter(io.Discard).Write(map[string]string{"x": at + "a"}); err == nil {
		t.Fatal("over 1 MB must fail")
	}
}

func TestInboundCap(t *testing.T) {
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], MaxInbound+1)
	if _, err := Read(bytes.NewReader(hdr[:])); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("want too large, got %v", err)
	}
}
