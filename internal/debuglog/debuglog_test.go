package debuglog

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCreatesPrivateAndRotates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "debug.log")
	if err := Append(p, "hello\n", 100); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %o", st.Mode().Perm())
		}
	}
	line := strings.Repeat("x", 40) + "\n"
	for i := 0; i < 50; i++ {
		if err := Append(p, line, 100); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{p, p + ".1"} {
		st, err := os.Stat(f)
		if err != nil || st.Size() > 100 {
			t.Fatalf("%s: %v size %d", f, err, st.Size())
		}
	}
}
