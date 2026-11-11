package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("CLAUDEZILLA_IPC_DIR", t.TempDir())
	t.Setenv("CLAUDEZILLA_AGENT_TIMEOUT_MS", "0")
	s, err := New(Options{Log: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDomainAllowlist(t *testing.T) {
	s := newTestServer(t)
	if got := s.checkDomain("https://anything.test/"); got != "" {
		t.Fatal("no allowlist must allow everything")
	}
	s.callTool("firefox_set_config", map[string]any{"allowedDomains": []any{"example.com", "*.docs.test"}})
	for u, ok := range map[string]bool{
		"https://example.com/x": true, "https://evil.com/": false, "https://a.docs.test/": true,
		"https://docs.test/": true, "https://a.b.docs.test/": false, "https://notdocs.test/": false,
		"about:blank": true,
	} {
		if got := s.checkDomain(u); (got == "") != ok {
			t.Errorf("%s: %q", u, got)
		}
	}
	s.callTool("firefox_set_config", map[string]any{"allowedDomains": []any{"bad pattern!"}})
	if got := s.checkDomain("https://x.test/"); !strings.Contains(got, "Invalid domain pattern") {
		t.Fatal(got)
	}
}

func TestDiffPageState(t *testing.T) {
	var before, after any
	_ = json.Unmarshal([]byte(`{"headings":[{"t":"A"},{"t":"B"}],"links":[]}`), &before)
	_ = json.Unmarshal([]byte(`{"headings":[{"t":"B"},{"t":"C"}],"links":[{"h":"/x"}]}`), &after)
	got := diffPageState(before, after)
	want := `{"added":{"headings":[{"t":"C"}],"links":[{"h":"/x"}]},"removed":{"headings":[{"t":"A"}]},"changed":{}}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestSaveScreenshotConfinement(t *testing.T) {
	data := []byte("img")
	dir, _ := os.MkdirTemp("", "czs")
	defer os.RemoveAll(dir)
	for _, bad := range []string{"/etc/x.png", filepath.Join(dir, "x.gif"), dir + "/../x.png", "/nonexistent-dir/x.png"} {
		if _, err := saveScreenshot(data, bad, "png"); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	p := filepath.Join(dir, "shot.png")
	if _, err := saveScreenshot(data, p, "png"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	if _, err := saveScreenshot(data, p, "png"); err == nil {
		t.Fatal("existing file must not be overwritten")
	}
	link := filepath.Join(dir, "link.png")
	_ = os.Symlink(filepath.Join(dir, "target.png"), link)
	if _, err := saveScreenshot(data, link, "png"); err == nil {
		t.Fatal("must not follow a symlink")
	}
}

func TestWithoutKeysKeepsOrder(t *testing.T) {
	got, _ := withoutKeys(json.RawMessage(`{"z":1,"text":"t","a":{"b":2}}`), "text")
	if string(got) != `{"z":1,"a":{"b":2}}` {
		t.Fatal(string(got))
	}
}
