package mcp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
)

func hostnameOf(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "", false
	}
	return strings.ToLower(u.Hostname()), true
}

// ---------------------------------------------------------------------------
// Screenshots

const screenshotKeep = 50

type screenshotFiles struct {
	enabled bool
	agentID string
}

// allowedSaveRoots: temp dir, /tmp and the working directory, as real paths
// so a symlink can't widen them.
func allowedSaveRoots() []string {
	cands := []string{os.TempDir()}
	if wd, err := os.Getwd(); err == nil {
		cands = append(cands, wd)
	}
	if runtime.GOOS != "windows" {
		cands = append(cands, "/tmp")
	}
	var roots []string
	for _, c := range cands {
		if r, err := filepath.EvalSymlinks(c); err == nil && !slices.Contains(roots, r) {
			roots = append(roots, r)
		}
	}
	return roots
}

// writePrivateFile creates path 0600 without following a symlink at the
// final component; exclusive refuses to replace an existing file.
func writePrivateFile(path string, data []byte, exclusive bool) error {
	flags := os.O_WRONLY | os.O_CREATE | noFollow
	if exclusive {
		flags |= os.O_EXCL
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// saveScreenshot writes to an agent-chosen savePath, confined to
// allowedSaveRoots and created exclusively.
func saveScreenshot(data []byte, savePath, format string) (string, error) {
	switch {
	case savePath == "":
		return "", errors.New("savePath must be a non-empty string")
	case strings.ContainsRune(savePath, 0):
		return "", errors.New("savePath contains null byte")
	case strings.Contains(savePath, ".."):
		return "", errors.New("savePath contains path traversal")
	}
	abs, err := filepath.Abs(savePath)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(abs))
	valid := []string{".png"}
	if format == "jpeg" {
		valid = []string{".jpg", ".jpeg"}
	}
	if !slices.Contains(valid, ext) {
		return "", errors.New("savePath must end in .jpg/.jpeg for JPEG or .png for PNG")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("savePath directory does not exist: %s", filepath.Dir(abs))
	}
	roots := allowedSaveRoots()
	inside := false
	for _, r := range roots {
		if parent == r || strings.HasPrefix(parent, strings.TrimSuffix(r, string(filepath.Separator))+string(filepath.Separator)) {
			inside = true
		}
	}
	if !inside {
		return "", fmt.Errorf("savePath must be inside one of: %s", strings.Join(roots, ", "))
	}
	if err := writePrivateFile(filepath.Join(parent, filepath.Base(abs)), data, true); err != nil {
		return "", err
	}
	return abs, nil
}

// write keeps a copy for harnesses that can't show inline images
// (CLAUDEZILLA_SCREENSHOT_FILES=1), pruned to the newest screenshotKeep.
func (sf screenshotFiles) write(data []byte, format string) (string, error) {
	dir := filepath.Join(os.TempDir(), "claudezilla-screenshots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !st.IsDir() || !ownedByMe(st) {
		return "", fmt.Errorf("Refusing to use %s: not a directory owned by this user", dir)
	}
	_ = os.Chmod(dir, 0o700)
	ext := "png"
	if format == "jpeg" {
		ext = "jpg"
	}
	file := filepath.Join(dir, fmt.Sprintf("%d-%s.%s", time.Now().UnixMilli(), sf.agentID[6:14], ext))
	if err := writePrivateFile(file, data, false); err != nil {
		return "", err
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > screenshotKeep {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		sort.Strings(names)
		for _, n := range names[:len(names)-screenshotKeep] {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
	return file, nil
}

func (s *Server) screenshotResult(result json.RawMessage, savePath string, hasSavePath bool) (toolResult, bool) {
	var r struct {
		DataURL string `json:"dataUrl"`
	}
	if json.Unmarshal(result, &r) != nil || r.DataURL == "" {
		return toolResult{}, false
	}
	mime, format := "image/png", "png"
	if strings.HasPrefix(r.DataURL, "data:image/jpeg") {
		mime, format = "image/jpeg", "jpeg"
	}
	b64 := r.DataURL[strings.IndexByte(r.DataURL, ',')+1:]
	meta, err := withoutKeys(result, "dataUrl")
	if err != nil {
		return toolResult{}, false
	}
	var saved string
	if hasSavePath || s.screenshots.enabled {
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return errorResult("Error: invalid screenshot data: " + err.Error()), true
		}
		if hasSavePath {
			saved, err = saveScreenshot(data, savePath, format)
		} else {
			saved, err = s.screenshots.write(data, format)
		}
		if err != nil {
			return errorResult("Error: " + err.Error()), true
		}
		fs, _ := objectFields(meta)
		fs = append(fs, field{"savedPath", json.RawMessage(marshal(saved))})
		meta = encodeFields(fs)
	}
	return toolResult{Content: []content{
		{Type: "text", Text: string(meta)},
		{Type: "image", Data: b64, MimeType: mime},
	}}, true
}

// ---------------------------------------------------------------------------
// Diagnostics

type check map[string]any

func (s *Server) diagnose() any {
	type report struct {
		Timestamp      string  `json:"timestamp"`
		SocketPath     string  `json:"socketPath"`
		AuthTokenPath  string  `json:"authTokenPath"`
		Checks         []check `json:"checks"`
		Summary        string  `json:"summary"`
		Recommendation string  `json:"recommendation"`
	}
	d := report{Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), SocketPath: s.paths.Socket, AuthTokenPath: s.paths.Token}

	sockSt, sockErr := os.Stat(s.paths.Socket)
	socketExists := sockErr == nil
	var perms any
	if socketExists {
		perms = fmt.Sprintf("%o", sockSt.Mode().Perm())
	}
	status := func(ok bool, yes, no string) string {
		if ok {
			return yes
		}
		return no
	}
	d.Checks = append(d.Checks, check{"name": "socket_file", "status": status(socketExists, "ok", "missing"), "path": s.paths.Socket, "permissions": perms})

	tokenBytes, tokErr := os.ReadFile(s.paths.Token)
	tokenExists := tokErr == nil || !errors.Is(tokErr, os.ErrNotExist)
	var tokenLen any
	if tokenExists {
		if tokErr != nil {
			tokenLen = "error: " + tokErr.Error()
		} else {
			tokenLen = len(strings.TrimSpace(string(tokenBytes)))
		}
	}
	d.Checks = append(d.Checks, check{"name": "auth_token", "status": status(tokenExists, "ok", "missing"), "path": s.paths.Token, "tokenLength": tokenLen})

	pingOK := false
	if socketExists && tokenExists {
		start := time.Now()
		resp, err := s.sendCommandOnce("ping", map[string]any{}, true, 0)
		if err != nil {
			d.Checks = append(d.Checks, check{"name": "connection_ping", "status": "failed", "error": err.Error()})
		} else {
			pingOK = resp.Success
			d.Checks = append(d.Checks, check{"name": "connection_ping", "status": status(resp.Success, "ok", "failed"), "latencyMs": time.Since(start).Milliseconds(), "result": resp.Result})
		}
	} else {
		d.Checks = append(d.Checks, check{"name": "connection_ping", "status": "skipped", "reason": status(!socketExists, "socket missing", "auth token missing")})
	}
	if pingOK {
		if resp, err := s.sendCommandOnce("version", map[string]any{}, true, 0); err != nil {
			d.Checks = append(d.Checks, check{"name": "extension_version", "status": "failed", "error": err.Error()})
		} else {
			d.Checks = append(d.Checks, check{"name": "extension_version", "status": "ok", "version": resp.Result})
		}
		if resp, err := s.sendCommandOnce("getTabs", map[string]any{}, true, 0); err != nil {
			d.Checks = append(d.Checks, check{"name": "tab_pool", "status": "failed", "error": err.Error()})
		} else {
			var t struct{ TabCount, MaxTabs int }
			_ = json.Unmarshal(resp.Result, &t)
			if t.MaxTabs == 0 {
				t.MaxTabs = 12
			}
			d.Checks = append(d.Checks, check{"name": "tab_pool", "status": "ok", "tabCount": t.TabCount, "maxTabs": t.MaxTabs})
		}
	}

	var failed []string
	for _, c := range d.Checks {
		if c["status"] == "failed" || c["status"] == "missing" {
			failed = append(failed, c["name"].(string))
		}
	}
	switch {
	case len(failed) == 0:
		d.Summary, d.Recommendation = "healthy", "Claudezilla is connected and working properly."
	case !socketExists && !tokenExists:
		d.Summary, d.Recommendation = "disconnected", "Claudezilla host not running. Click the Claudezilla extension icon in Firefox to reconnect."
	case !socketExists:
		d.Summary, d.Recommendation = "partial", "Socket missing but auth token exists. The host may have crashed. Reload the Claudezilla extension."
	case !tokenExists:
		d.Summary, d.Recommendation = "partial", "Auth token missing. The host may not have started correctly. Reload the Claudezilla extension."
	default:
		d.Summary, d.Recommendation = "error", fmt.Sprintf("Connection issues detected: %s. Try reloading the Claudezilla extension.", strings.Join(failed, ", "))
	}
	return d
}
