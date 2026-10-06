package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// localNames maps every server-side input path to a unique local relative
// path that is valid on Windows (05-worker.md «Рабочая папка»).
//
// The mapping is stable: it lives in .studlance/localnames.json and is reused
// between continue/retry, so a file never changes its on-disk name after a
// worker restart. Uniqueness is case-insensitive (Windows filesystems compare
// file names without case) and covers folder segments as well as file names.
//
// One mapping is used everywhere a server path turns into a local one:
// downloading, TASK.md, the {{.Files}} prompt block and REVISION-<n>.md.
type localNames struct {
	path string

	mu      sync.Mutex
	byPath  map[string]string // server rel path -> local rel path
	byLocal map[string]string // strings.ToLower(local rel path) -> server rel path
}

// loadLocalNames reads the persisted mapping; a missing or broken file just
// yields an empty mapping (it is rebuilt deterministically).
func loadLocalNames(path string) *localNames {
	m := &localNames{
		path:    path,
		byPath:  map[string]string{},
		byLocal: map[string]string{},
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	var byPath map[string]string
	if err := json.Unmarshal(raw, &byPath); err != nil {
		return m
	}
	for server, local := range byPath {
		if server == "" || local == "" {
			continue
		}
		m.byPath[server] = local
		m.byLocal[strings.ToLower(local)] = server
	}
	return m
}

// assignAll gives every server path a local name, creating new entries for
// the paths that are not mapped yet. New names are chosen in sorted order, so
// the result does not depend on the order the server listed the files in.
func (m *localNames) assignAll(serverPaths []string) {
	if m == nil {
		return
	}
	paths := append([]string(nil), serverPaths...)
	sort.Strings(paths)

	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for _, sp := range paths {
		if sp == "" {
			continue
		}
		if _, ok := m.byPath[sp]; ok {
			continue
		}
		local := m.uniqueLocked(sp)
		m.byPath[sp] = local
		m.byLocal[strings.ToLower(local)] = sp
		changed = true
	}
	if changed {
		m.saveLocked()
	}
}

// local returns the stable local name of a server path, assigning one if it
// is not known yet.
func (m *localNames) local(server string) string {
	if m == nil {
		return mapLocalName(server)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if local, ok := m.byPath[server]; ok {
		return local
	}
	local := m.uniqueLocked(server)
	m.byPath[server] = local
	m.byLocal[strings.ToLower(local)] = server
	m.saveLocked()
	return local
}

// uniqueLocked picks a free local name for server: the base Windows-safe
// mapping when it is free, otherwise the base with a "~N" suffix before the
// extension of the last segment. Callers hold m.mu.
func (m *localNames) uniqueLocked(server string) string {
	base := mapLocalName(server)
	candidate := base
	for n := 2; ; n++ {
		if _, taken := m.byLocal[strings.ToLower(candidate)]; !taken {
			return candidate
		}
		candidate = withSuffix(base, n)
	}
}

// withSuffix inserts "~N" before the extension of the last path segment:
// "отчёт.docx" -> "отчёт~2.docx", "папка/файл" -> "папка/файл~2".
func withSuffix(rel string, n int) string {
	segs := strings.Split(rel, "/")
	last := segs[len(segs)-1]
	base, ext := last, ""
	// A leading dot is not an extension separator (.gitignore stays whole).
	if i := strings.LastIndexByte(last, '.'); i > 0 {
		base, ext = last[:i], last[i:]
	}
	segs[len(segs)-1] = fmt.Sprintf("%s~%d%s", base, n, ext)
	return strings.Join(segs, "/")
}

// saveLocked writes the mapping atomically. Callers hold m.mu.
func (m *localNames) saveLocked() {
	if m.path == "" {
		return
	}
	raw, err := json.MarshalIndent(m.byPath, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.path), ".localnames-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return
	}
	if err := os.Rename(name, m.path); err != nil {
		_ = os.Remove(name)
	}
}
