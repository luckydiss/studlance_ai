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
// file names without case) and tree-aware: an assigned local path is either a
// file or a directory, a file can never be a directory, and a file can never
// be a parent of another path. Collisions are resolved with a stable "~N"
// suffix in the conflicting segment.
//
// One mapping is used everywhere a server path turns into a local one:
// downloading, TASK.md, the {{.Files}} prompt block and REVISION-<n>.md.
type localNames struct {
	path string

	mu     sync.Mutex
	byPath map[string]string // server rel path -> local rel path
	files  map[string]string // strings.ToLower(local file path) -> server rel path
	dirs   map[string]bool   // strings.ToLower(local dir path) -> true
}

// loadLocalNames reads the persisted mapping and repairs it when an older
// worker left a file/directory collision behind: entries are taken in sorted
// server-path order, a correct entry keeps its name and a conflicting one is
// renamed and saved. A missing or broken file yields an empty mapping.
func loadLocalNames(path string) *localNames {
	m := &localNames{
		path:   path,
		byPath: map[string]string{},
		files:  map[string]string{},
		dirs:   map[string]bool{},
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	var stored map[string]string
	if err := json.Unmarshal(raw, &stored); err != nil {
		return m
	}
	servers := make([]string, 0, len(stored))
	for server := range stored {
		if server != "" {
			servers = append(servers, server)
		}
	}
	sort.Strings(servers)

	changed := false
	for _, server := range servers {
		local := stored[server]
		if local == "" {
			changed = true
			continue
		}
		if m.conflictIndex(local) >= 0 {
			// A broken map from an older worker: the path cannot be used as
			// is, pick a safe name instead of dropping the file.
			local = m.uniqueLocked(server)
			changed = true
		}
		m.registerLocked(server, local)
	}
	if changed {
		m.saveLocked()
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
	seen := make(map[string]bool, len(paths))
	for _, sp := range paths {
		if sp == "" || seen[sp] {
			continue
		}
		seen[sp] = true
		if _, ok := m.byPath[sp]; ok {
			continue
		}
		m.registerLocked(sp, m.uniqueLocked(sp))
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
	m.registerLocked(server, local)
	m.saveLocked()
	return local
}

// registerLocked records a server->local mapping and the tree nodes it
// implies: the full path is a file, every proper prefix is a directory.
// Callers hold m.mu.
func (m *localNames) registerLocked(server, local string) {
	m.byPath[server] = local
	segs := strings.Split(local, "/")
	m.files[strings.ToLower(local)] = server
	for i := 1; i < len(segs); i++ {
		p := strings.ToLower(strings.Join(segs[:i], "/"))
		if _, isFile := m.files[p]; !isFile {
			m.dirs[p] = true
		}
	}
}

// conflictIndex reports the index of the first segment that makes local
// unusable in the current tree, or -1 when the path is free. A path conflicts
// when a directory it needs is already a file, when the path itself is
// already a file (case-insensitively), or when the path is already a
// directory (a file cannot be a folder).
func (m *localNames) conflictIndex(local string) int {
	segs := strings.Split(local, "/")
	for i := 1; i < len(segs); i++ {
		p := strings.ToLower(strings.Join(segs[:i], "/"))
		if _, isFile := m.files[p]; isFile {
			return i - 1
		}
	}
	key := strings.ToLower(local)
	if _, isFile := m.files[key]; isFile {
		return len(segs) - 1
	}
	if m.dirs[key] {
		return len(segs) - 1
	}
	return -1
}

// uniqueLocked picks a free local name for server: the base Windows-safe
// mapping when it is free, otherwise the same path with a "~N" suffix in the
// conflicting segment (re-checked after every attempt, because renaming a
// segment can expose another collision). Callers hold m.mu.
func (m *localNames) uniqueLocked(server string) string {
	segs := strings.Split(mapLocalName(server), "/")
	suffix := make([]int, len(segs))
	for attempt := 0; attempt < 1<<20; attempt++ {
		candidate := joinSegments(segs, suffix)
		idx := m.conflictIndex(candidate)
		if idx < 0 {
			return candidate
		}
		if suffix[idx] < 2 {
			suffix[idx] = 2
		} else {
			suffix[idx]++
		}
	}
	return joinSegments(segs, suffix)
}

// joinSegments applies the per-segment suffix numbers (0 = unchanged).
func joinSegments(segs []string, suffix []int) string {
	out := make([]string, len(segs))
	for i, seg := range segs {
		if suffix[i] >= 2 {
			out[i] = segmentWithSuffix(seg, suffix[i])
		} else {
			out[i] = seg
		}
	}
	return strings.Join(out, "/")
}

// segmentWithSuffix inserts "~N" before the extension of one path segment:
// "отчёт.docx" -> "отчёт~2.docx", "папка" -> "папка~2".
func segmentWithSuffix(seg string, n int) string {
	base, ext := seg, ""
	// A leading dot is not an extension separator (.gitignore stays whole).
	if i := strings.LastIndexByte(seg, '.'); i > 0 {
		base, ext = seg[:i], seg[i:]
	}
	return fmt.Sprintf("%s~%d%s", base, n, ext)
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
