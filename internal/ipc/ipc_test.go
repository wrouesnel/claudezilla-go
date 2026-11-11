package ipc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidatePath(t *testing.T) {
	if err := ValidatePath("/run/user/1000/x", "p"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "a\x00b", "/a/../b"} {
		if err := ValidatePath(bad, "ctx"); err == nil || !strings.Contains(err.Error(), "ctx") {
			t.Fatalf("%q: want error naming context, got %v", bad, err)
		}
	}
	if runtime.GOOS != "windows" {
		if err := ValidatePath("//double", "p"); err != nil {
			t.Fatal("double slash is only blocked on Windows:", err)
		}
	}
}

func TestCommandTimeout(t *testing.T) {
	cases := []struct {
		params map[string]any
		want   int
	}{
		{map[string]any{"_timeout": 5000.0}, 5000},
		{map[string]any{"_timeout": 300000.0}, 300000},
		{map[string]any{"_timeout": 4999.0}, DefaultCommandTimeoutMs},
		{map[string]any{"_timeout": 300001.0}, DefaultCommandTimeoutMs},
		{map[string]any{"_timeout": "60000"}, DefaultCommandTimeoutMs},
		{map[string]any{}, DefaultCommandTimeoutMs},
		{nil, DefaultCommandTimeoutMs},
	}
	for _, c := range cases {
		if got := CommandTimeoutMs(c.params); got != c.want {
			t.Errorf("%v: got %d want %d", c.params, got, c.want)
		}
		if got := SocketTimeoutMs(c.params); got != c.want+SocketTimeoutGraceMs {
			t.Errorf("%v: socket timeout %d", c.params, got)
		}
	}
}

func TestQuitBrowserAllowed(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, "TRUE": true, "": false, "0": false, "yes": false} {
		t.Setenv("CLAUDEZILLA_ALLOW_QUIT", v)
		if QuitBrowserAllowed() != want {
			t.Errorf("%q: want %v", v, want)
		}
	}
}

func TestIPCDirOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := filepath.Join(t.TempDir(), "ipc")
	t.Setenv("CLAUDEZILLA_IPC_DIR", dir)
	p, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if p.Socket != filepath.Join(dir, SocketName) || p.Token != filepath.Join(dir, TokenName) || p.DebugLog != filepath.Join(dir, DebugLogName) {
		t.Fatalf("paths not in override: %+v", p)
	}
	st, _ := os.Stat(dir)
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("created with %o", st.Mode().Perm())
	}
	_ = os.Chmod(dir, 0o755)
	if _, err := Resolve(); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatalf("existing dir not chmod'ed: %o", st.Mode().Perm())
	}
}

func TestIPCDirInvalidOverrideFallsBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	xdg := t.TempDir()
	t.Setenv("CLAUDEZILLA_IPC_DIR", "/tmp/../etc")
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	p, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if p.Dir != xdg {
		t.Fatalf("want fallback to XDG_RUNTIME_DIR, got %s", p.Dir)
	}
}

func TestNeverSharedTemp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	t.Setenv("CLAUDEZILLA_IPC_DIR", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", "/nonexistent/../nope")
	if d, err := Dir(); err == nil && (d == os.TempDir() || d == "/tmp") {
		t.Fatalf("fell back to shared temp dir %s", d)
	}
}
