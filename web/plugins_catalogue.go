// Plugin catalogue + installer.
//
// The remote catalogue is the same index.json the boot-animation themes already
// use (zhoujiannan-nb/mini_code-plugins), extended with more kinds:
//
//	{"schema":1,"repo":"...","ref":"main","kinds":{
//	   "boot-anim":[{"id":..., "path":"boot-anim/x", "files":[{"path":..., "sha256":...}]}],
//	   "tool":     [...],
//	   "process":  [...],
//	   "skin":     [...],
//	   "wallpaper":[...],
//	   "pet":      [...]
//	}}
//
// Adding a kind is therefore a pure data change in that repo: no protocol bump,
// no new endpoint. Everything a catalogue entry says is verified before it lands
// (sha256 per file, path containment, extension whitelist), and the swap is the
// same staging-directory + rename the boot animation installer uses, so a failed
// download can never leave a half-installed plugin behind.
package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/user/mini_code/config"
	"github.com/user/mini_code/plugin"
)

const (
	// pluginCatalogueTTL is how long a fetched index.json is reused. Long enough
	// that opening the centre repeatedly is free, short enough that a new
	// release shows up on the next visit.
	pluginCatalogueTTL    = 5 * time.Minute
	pluginCatalogueMaxAge = 5 * time.Second
	// pluginMaxFiles / pluginMaxFileBytes / pluginMaxTotalBytes bound what one
	// catalogue entry may install. A skin is a few hundred KB; a process plugin
	// is a script. Anything near these numbers is a mistake or an attack.
	pluginMaxFiles      = 40
	pluginMaxFileBytes  = 8 << 20
	pluginMaxTotalBytes = 24 << 20
	// catalogueMarker is written into an installed plugin directory. Its presence
	// is what lets the centre call a plugin "installed from the catalogue" and
	// lets the uninstall path know the directory is ours to delete.
	catalogueMarker = ".catalogue"

	// catalogueFetchTimeout bounds one raw-file request and the index fetch.
	catalogueFetchTimeout = 12 * time.Second
)

// installMu serializes installs: two of them at once would both try to rename
// the same target directory.
var installMu sync.Mutex

// randSuffix is a short random string used to make staging/old directory names
// unique, so two interrupted installs can never collide.
func randSuffix() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// oldExists reports whether a path exists (used before moving a version aside).
func oldExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// pluginCatalogueIndex mirrors the parts of index.json this host understands.
// Unknown fields (colours, durationMs, preview…) are ignored: they are boot
// animation metadata.
type pluginCatalogueIndex struct {
	Schema int                              `json:"schema"`
	Repo   string                           `json:"repo"`
	Ref    string                           `json:"ref"`
	Kinds  map[string][]pluginCatalogueItem `json:"kinds"`
}

// pluginCatalogueItem is one installable entry.
type pluginCatalogueItem struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	NameEn      string   `json:"nameEn,omitempty"`
	Author      string   `json:"author,omitempty"`
	Version     string   `json:"version,omitempty"`
	License     string   `json:"license,omitempty"`
	Description string   `json:"description,omitempty"`
	Path        string   `json:"path"`
	Entry       string   `json:"entry,omitempty"`
	Preview     string   `json:"preview,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Category    string   `json:"category,omitempty"`
	Files       []struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

// catalogueEntry is one row for the UI: the item plus what is already installed.
type catalogueEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Author      string   `json:"author,omitempty"`
	Version     string   `json:"version,omitempty"`
	Description string   `json:"description,omitempty"`
	Kind        string   `json:"kind"`
	Category    string   `json:"category"`
	License     string   `json:"license,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Installed   bool     `json:"installed"`
	UpToDate    bool     `json:"up_to_date"`
	InstalledV  string   `json:"installed_version,omitempty"`
	Files       int      `json:"files"`
	Bytes       int64    `json:"bytes"`
}

// pluginCatalogueCache memoizes index.json. Failures are cached for a much
// shorter time so one offline click does not make the button look broken.
var pluginCatalogueCache = struct {
	mu    sync.Mutex
	key   string
	at    time.Time
	idx   *pluginCatalogueIndex
	err   error
	errAt time.Time
}{}

// pluginCatalogueRepo is the GitHub "owner/name" the catalogue comes from. It
// reuses the boot animation setting on purpose: one place to point mini_code at
// a fork, and the plugin repo already holds both kinds.
func (s *server) pluginCatalogueRepo() string {
	if cfg := s.be.GetConfig(); cfg != nil {
		b := cfg.BootAnim
		b.Normalize()
		return b.EffectiveRepo()
	}
	return config.DefaultBootAnimRepo
}

// remotePluginCatalogue fetches (or returns the cached) index.json.
func (s *server) remotePluginCatalogue() (*pluginCatalogueIndex, error) {
	repo := s.pluginCatalogueRepo()
	if !config.ValidBootAnimRepo(repo) {
		return nil, fmt.Errorf("插件仓库格式应为 owner/name,当前是 %q", repo)
	}
	url := bootAnimRawURL(repo, "main", "index.json")

	pluginCatalogueCache.mu.Lock()
	defer pluginCatalogueCache.mu.Unlock()
	if pluginCatalogueCache.key == url && !pluginCatalogueCache.at.IsZero() &&
		time.Since(pluginCatalogueCache.at) < pluginCatalogueTTL {
		return pluginCatalogueCache.idx, nil
	}
	if pluginCatalogueCache.key == url && pluginCatalogueCache.err != nil &&
		time.Since(pluginCatalogueCache.errAt) < pluginCatalogueMaxAge {
		return nil, pluginCatalogueCache.err
	}
	idx, err := fetchPluginCatalogue(url)
	if err != nil {
		pluginCatalogueCache.key, pluginCatalogueCache.err, pluginCatalogueCache.errAt = url, err, time.Now()
		return nil, err
	}
	pluginCatalogueCache.key, pluginCatalogueCache.idx, pluginCatalogueCache.at = url, idx, time.Now()
	pluginCatalogueCache.err = nil
	return idx, nil
}

// fetchPluginCatalogue downloads and loosely validates index.json. The boot
// animation fetcher already parses the same file; this one keeps the extra kinds
// and tolerates a schema it has never seen.
func fetchPluginCatalogue(url string) (*pluginCatalogueIndex, error) {
	client := &http.Client{Timeout: catalogueFetchTimeout}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mini_code")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法访问插件仓库(需要能访问 raw.githubusercontent.com): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("插件仓库里没有 index.json(确认仓库地址和 main 分支)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("插件仓库返回 HTTP %d", resp.StatusCode)
	}
	var idx pluginCatalogueIndex
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&idx); err != nil {
		return nil, fmt.Errorf("插件仓库的 index.json 解析失败: %v", err)
	}
	if !bootAnimRefRe.MatchString(idx.Ref) {
		idx.Ref = "main"
	}
	if idx.Kinds == nil {
		idx.Kinds = map[string][]pluginCatalogueItem{}
	}
	return &idx, nil
}

// catalogueKindCategory maps an index.json bucket to the centre's category.
func catalogueKindCategory(kind string) string {
	switch strings.ToLower(kind) {
	case "process", "program", "service", "daemon":
		return plugin.CategoryProcess
	case "tool", "tools", "mcp", "skill":
		return plugin.CategoryTool
	case "skin", "wallpaper", "pet", "anim", "boot-anim", "appearance", "theme":
		return plugin.CategoryAppearance
	}
	return plugin.CategoryAppearance
}

// handlePluginCatalog: GET /api/plugins/catalog — the remote catalogue, merged
// across kinds and annotated with what is already installed.
func (s *server) handlePluginCatalog(w http.ResponseWriter, r *http.Request) {
	idx, err := s.remotePluginCatalogue()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	have := map[string]string{} // id → installed version ("" = installed, unknown version)
	for _, p := range plugin.Discover() {
		if p.LoadError == "" {
			have[p.ID] = p.Version
		}
	}
	entries := make([]catalogueEntry, 0, 16)
	kinds := make([]string, 0, len(idx.Kinds))
	for k := range idx.Kinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds) // deterministic order: the UI groups by category anyway
	// 一个 id 只出现一次。index.json 允许同一批插件挂在两个桶里(仓库给老宿主留的
	// "别名桶",比如 appearance 之外再来一份 boot-anim),市场要的是"装哪个",
	// 不是"它声明了几次" —— 不去重的话同一段开机动画会并列出两张卡。
	seen := map[string]bool{}
	for _, kind := range kinds {
		for _, it := range idx.Kinds[kind] {
			if !validPluginID(it.ID) || it.Path == "" {
				slog.Warn("plugin catalogue: skipping invalid entry", "kind", kind, "id", it.ID)
				continue
			}
			if seen[it.ID] {
				// 别名桶排在后面(桶名有序),所以先命中的那份说了算。
				slog.Debug("plugin catalogue: deduplicated entry", "kind", kind, "id", it.ID)
				continue
			}
			seen[it.ID] = true
			var total int64
			for _, f := range it.Files {
				total += f.Size
			}
			cur, installed := have[it.ID]
			category := plugin.CategoryAppearance
			if it.Category != "" {
				category = it.Category
			} else {
				category = catalogueKindCategory(kind)
			}
			entries = append(entries, catalogueEntry{
				ID:          it.ID,
				Name:        firstNonEmpty(it.Name, it.ID),
				Author:      it.Author,
				Version:     it.Version,
				Description: it.Description,
				Kind:        kind,
				Category:    category,
				License:     it.License,
				Tags:        it.Tags,
				Installed:   installed,
				UpToDate:    installed && cur == it.Version,
				InstalledV:  cur,
				Files:       len(it.Files),
				Bytes:       total,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"repo":       s.pluginCatalogueRepo(),
		"ref":        idx.Ref,
		"fetched_at": time.Now().Unix(),
		"cached_for": int(pluginCatalogueTTL.Seconds()),
		"entries":    entries,
	})
}

// handlePluginInstall: POST /api/plugins/install {"id": "...", "kind": "..."}.
//
// Every file is downloaded to a staging directory, its size and sha256 compared
// with the catalogue, and only then is the staging directory renamed into place.
// An existing version is moved aside and restored if the new one does not load.
func (s *server) handlePluginInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if !validPluginID(req.ID) {
		writeErr(w, http.StatusBadRequest, "非法的插件 id")
		return
	}
	idx, err := s.remotePluginCatalogue()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	item, kind := findCatalogueItem(idx, req.ID, req.Kind)
	if item == nil {
		writeErr(w, http.StatusNotFound, "插件仓库里没有这个插件")
		return
	}
	root, err := plugin.DirPath()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	rawBase := bootAnimRawBase(s.pluginCatalogueRepo(), idx.Ref)
	installed, err := installCatalogPlugin(root, rawBase, kind, item)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "已安装 " + installed.Name,
		"plugin": map[string]any{
			"id":       installed.ID,
			"name":     installed.Name,
			"version":  installed.Version,
			"category": installed.Category(),
			"kinds":    installed.Kinds(),
		},
	})
}

// findCatalogueItem locates one entry, optionally restricted to a kind.
func findCatalogueItem(idx *pluginCatalogueIndex, id, kind string) (*pluginCatalogueItem, string) {
	kinds := make([]string, 0, len(idx.Kinds))
	for k := range idx.Kinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		if kind != "" && !strings.EqualFold(k, kind) {
			continue
		}
		for i := range idx.Kinds[k] {
			if idx.Kinds[k][i].ID == id {
				it := idx.Kinds[k][i]
				return &it, k
			}
		}
	}
	// An id may exist under a different kind than the one asked for (a skin that
	// was recategorised). Retry unrestricted so the install still works.
	if kind != "" {
		return findCatalogueItem(idx, id, "")
	}
	return nil, ""
}

// installCatalogPlugin is the atomic install: stage → verify → rename → validate
// → (roll back on failure).
func installCatalogPlugin(root, rawBase, kind string, item *pluginCatalogueItem) (*plugin.Plugin, error) {
	if !validPluginID(item.ID) {
		return nil, fmt.Errorf("非法的插件 id %q", item.ID)
	}
	if len(item.Files) == 0 || len(item.Files) > pluginMaxFiles {
		return nil, fmt.Errorf("插件文件数不合法(%d)", len(item.Files))
	}
	// The manifest has to be in the bundle: without it the directory is not a
	// plugin, and we would install something the host cannot even describe.
	var haveManifest bool
	for _, f := range item.Files {
		if path.Base(path.Clean(f.Path)) == "plugin.json" &&
			path.Dir(path.Clean(f.Path)) == path.Clean(item.Path) {
			haveManifest = true
		}
	}
	if !haveManifest {
		return nil, errors.New("插件仓库的 index.json 里缺 plugin.json,无法安装")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("无法创建插件目录 %s: %w", root, err)
	}

	// One install at a time: two of them would race on the same target directory.
	installMu.Lock()
	defer installMu.Unlock()

	suffix := randSuffix()
	stage := filepath.Join(root, ".install-"+item.ID+"-"+suffix)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return nil, fmt.Errorf("无法创建临时目录: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.RemoveAll(stage)
		}
	}()

	client := &http.Client{Timeout: catalogueFetchTimeout}
	var total int64
	for _, f := range item.Files {
		name, err := catalogueBundleRelPath(item.Path, f.Path)
		if err != nil {
			return nil, err
		}
		if f.Size < 0 || f.Size > pluginMaxFileBytes {
			return nil, fmt.Errorf("%s 超过单个文件大小上限", name)
		}
		total += f.Size
		if total > pluginMaxTotalBytes {
			return nil, errors.New("插件总大小超过上限")
		}
		if err := downloadVerified(client, rawURL(rawBase, f.Path), filepath.Join(stage, filepath.FromSlash(name)),
			f.Size, f.SHA256); err != nil {
			return nil, err
		}
	}
	// Marker: this directory came from the catalogue, so the centre can show
	// "已安装" and uninstall is allowed to delete it.
	if err := os.WriteFile(filepath.Join(stage, catalogueMarker),
		[]byte(kind+"\n"+item.Version+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("无法写入安装标记: %w", err)
	}

	target := filepath.Join(root, item.ID)
	old := target + ".old-" + suffix
	if oldExists(target) {
		if err := os.Rename(target, old); err != nil {
			return nil, fmt.Errorf("旧版本无法让位(文件可能正被占用): %w", err)
		}
	}
	rollback := func() {
		os.RemoveAll(target)
		if oldExists(old) {
			os.Rename(old, target)
		}
	}
	if err := os.Rename(stage, target); err != nil {
		rollback()
		return nil, fmt.Errorf("安装失败: %w", err)
	}
	cleanup = false

	// Validate under the real name: the manifest's id must match the directory,
	// and every referenced file must exist. A bundle that cannot be loaded puts
	// the previous version back rather than leaving the user with a broken skin.
	installed, err := loadPluginFromDir(target, item.ID)
	if err != nil {
		rollback()
		return nil, fmt.Errorf("下载完成但插件不可用,已回滚: %w", err)
	}
	os.RemoveAll(old)
	slog.Info("plugin installed", "id", installed.ID, "kind", kind, "version", installed.Version,
		"files", len(item.Files), "bytes", total, "dir", target)
	return &installed, nil
}

// catalogueBundleRelPath maps a repo-root-relative catalogue path to the file
// name it gets inside the plugin directory, refusing anything outside it.
func catalogueBundleRelPath(itemPath, p string) (string, error) {
	clean := path.Clean(strings.TrimSpace(p))
	prefix := path.Clean(itemPath) + "/"
	if !strings.HasPrefix(clean, prefix) {
		return "", fmt.Errorf("插件文件路径不在插件目录内: %s", p)
	}
	name := strings.TrimPrefix(clean, prefix)
	if name == "" || !bootAnimRelPathOK(name) {
		return "", fmt.Errorf("非法的插件文件路径: %s", p)
	}
	if _, ok := catalogueDownloadExtTypes[strings.ToLower(path.Ext(name))]; !ok {
		return "", fmt.Errorf("不允许的插件文件类型: %s", name)
	}
	return name, nil
}

// catalogueDownloadExtTypes is what may be *downloaded*. It is deliberately
// wider than pluginAssetExtTypes, which is what may be *served*:
//
//   - .js / .mjs    a startup-animation plugin (boot block) animates with code.
//     It runs in a sandboxed iframe via /bootanim/…, and the normal
//     /plugins/ route still refuses .js, so nothing gains access to the app's
//     own origin by this being downloadable.
//   - audio (.mp3 …) a clip may carry voice cues (see bootanimExtTypes).
//   - .json / .md    manifests, memory notes and plugin data.
//   - .py / .sh      the implementation of a TOOL plugin (kind 2). Its whole
//     purpose is to run an external program: the manifest already names the
//     command ("python") and the host execs it, so refusing to download the
//     script does not remove any capability — it only forces authors to smuggle
//     the same code through `python -c "<10 KB of source>"` in the manifest.
//     Note this is download-only: the /plugins/ asset route still refuses .py.
//
// A file that is downloaded but never served is inert: the point of the
// whitelist is to keep junk and surprises out of the install, not to enumerate
// every route.
var catalogueDownloadExtTypes = func() map[string]bool {
	out := make(map[string]bool, len(pluginAssetExtTypes)+10)
	for ext := range pluginAssetExtTypes {
		out[ext] = true
	}
	for _, ext := range []string{".js", ".mjs", ".json", ".md", ".py", ".sh",
		".mp3", ".ogg", ".wav", ".ico"} {
		out[ext] = true
	}
	return out
}()

// rawURL joins a raw-file base and a repo-relative path.
func rawURL(rawBase, p string) string {
	return rawBase + path.Clean(strings.TrimSpace(p))
}

// downloadVerified fetches one raw file and checks its size and sha256 against
// the catalogue. A mismatch is fatal: the bundle is discarded.
func downloadVerified(client *http.Client, url, dst string, wantSize int64, wantSHA string) error {
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("下载失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 %s 时 HTTP %d", path.Base(dst), resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, pluginMaxFileBytes+1))
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("写入 %s 失败: %v", path.Base(dst), copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if n > pluginMaxFileBytes {
		return fmt.Errorf("%s 超过单个文件大小上限", path.Base(dst))
	}
	if wantSize > 0 && n != wantSize {
		return fmt.Errorf("%s 完整性校验失败:大小不符(期望 %d 字节,实际 %d)—— 已放弃安装,未写入任何文件",
			path.Base(dst), wantSize, n)
	}
	if wantSHA != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, wantSHA) {
			return fmt.Errorf("%s 完整性校验失败:sha256 不符(期望 %s…,实际 %s…)—— 已放弃安装,未写入任何文件",
				path.Base(dst), wantSHA[:min(12, len(wantSHA))], got[:12])
		}
	}
	return nil
}

// loadPluginFromDir re-reads one installed plugin directory through the plugin
// package's own loader, so the installer and the normal load path agree on what
// "valid" means.
func loadPluginFromDir(dir, id string) (plugin.Plugin, error) {
	return plugin.LoadDir(dir, id)
}
