package strmscrape

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"litepan/internal/domain"
	"litepan/internal/mediaorganize/rules"
)

var explicitSeasonEpisodeFileRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s\d{1,3}e\d{1,4}(?:[^a-z0-9]|$)`)

type strmEntry struct {
	absPath string
	relPath string
}

// workGroup 一部作品：电影文件夹或剧集根目录；扁平散落的单个 .strm 各自成组。
type workGroup struct {
	relKey   string // 相对库根的稳定键（目录或单个 strm 相对路径）
	absDir   string // 元数据写入目录（扁平时为库根）
	flatFile string // 非空表示扁平单文件作品，值为 .strm 绝对路径
	entries  []strmEntry
}

func scanStrmFiles(root string) ([]strmEntry, error) {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, domain.Errorf(domain.CodeValidation, "输出目录不是文件夹")
	}
	var out []strmEntry
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".strm") {
			return nil
		}
		out = append(out, strmEntry{absPath: path, relPath: relUnder(root, path)})
		return nil
	})
	return out, err
}

func groupWorks(root string, entries []strmEntry) []workGroup {
	byKey := make(map[string]*workGroup, len(entries))
	order := make([]string, 0, len(entries))
	for _, e := range entries {
		key, absDir, flat := workKeyForStrm(root, e)
		g, ok := byKey[key]
		if !ok {
			g = &workGroup{relKey: key, absDir: absDir, flatFile: flat}
			byKey[key] = g
			order = append(order, key)
		}
		g.entries = append(g.entries, e)
	}
	out := make([]workGroup, 0, len(order))
	for _, key := range order {
		g := byKey[key]
		sort.Slice(g.entries, func(i, j int) bool {
			return g.entries[i].relPath < g.entries[j].relPath
		})
		out = append(out, *g)
	}
	return out
}

func workKeyForStrm(root string, e strmEntry) (relKey, absDir, flatFile string) {
	workDir := resolveWorkDir(root, e.absPath)
	if sameFilePath(workDir, root) {
		// 直接散落在库根：每个 .strm 独立成一部作品，避免全部并成一张海报
		return filepath.ToSlash(e.relPath), root, e.absPath
	}
	return filepath.ToSlash(relUnder(root, workDir)), workDir, ""
}

// resolveWorkDir 从 .strm 所在目录向上跳过 Season / 特别篇目录，落到作品根。
func resolveWorkDir(libraryRoot, strmAbs string) string {
	dir := filepath.Dir(strmAbs)
	for {
		if !isInside(libraryRoot, dir) && !sameFilePath(dir, libraryRoot) {
			return filepath.Dir(strmAbs)
		}
		if sameFilePath(dir, libraryRoot) {
			return libraryRoot
		}
		// 整理成功的作品目录以 TMDB ID 作为强边界。即使电影标题包含
		// “第一部/第二部”等容易被旧季号规则误判的文本，也不能继续向上
		// 提升到“电影”分类目录并把全部影片合并成一部剧。
		if rules.FindTMDBIDInName(filepath.Base(dir)) != "" {
			return dir
		}
		if isStructuralWorkSubdir(dir) {
			parent := filepath.Dir(dir)
			if parent == dir || (!isInside(libraryRoot, parent) && !sameFilePath(parent, libraryRoot)) {
				return dir
			}
			dir = parent
			continue
		}
		return dir
	}
}

func inferMediaType(g workGroup) string {
	// 已整理目录由分类路径决定类型，标题和文件名只负责季集解析，不能反向
	// 覆盖分类结果。TMDB 的 movie/tv ID 分属不同命名空间，必须与类型配对。
	if mediaType, ok := organizedMediaType(g); ok {
		return mediaType
	}
	// 目录结构优先：存在 Season / 特别篇子目录，或文件位于此类目录下 → 剧集
	if g.flatFile == "" {
		if entries, err := os.ReadDir(g.absDir); err == nil {
			for _, d := range entries {
				if d.IsDir() && isStructuralWorkSubdir(filepath.Join(g.absDir, d.Name())) {
					return MediaTypeTV
				}
			}
		}
	}
	for _, e := range g.entries {
		parentDir := filepath.Dir(e.absPath)
		parent := filepath.Base(parentDir)
		if rules.IsSeasonDirName(parent) || (!sameFilePath(parentDir, g.absDir) && isStructuralWorkSubdir(parentDir)) {
			return MediaTypeTV
		}
	}

	seCount := 0
	for _, e := range g.entries {
		stem := strings.TrimSuffix(filepath.Base(e.absPath), filepath.Ext(e.absPath))
		if explicitSeasonEpisodeFileRe.MatchString(stem) {
			return MediaTypeTV
		}
		parsed := rules.NormalizeParsedMedia(rules.ParseFilenameStrict(stem + ".mkv"))
		if parsed.Season != nil && parsed.Episode != nil {
			seCount++
		}
	}
	// 多个解析为分集的文件也按剧集处理；两个以上可避开单个音轨标记误判。
	if seCount >= 2 {
		return MediaTypeTV
	}

	// 典型电影文件夹「片名 (年)」：避免单文件音轨标记（DTS5.1 / DDP2.0）误判成剧集
	folderName := workDisplayName(g)
	if isLikelyMovieWorkFolder(folderName) {
		return MediaTypeMovie
	}
	if seCount >= 1 {
		return MediaTypeTV
	}
	return MediaTypeMovie
}

// organizedMediaType 识别目录整理后的常见分类路径。只有作品目录本身带 TMDB
// 标记时才启用强类型，未整理的旧目录继续使用原有启发式兼容逻辑。
func organizedMediaType(g workGroup) (string, bool) {
	if g.flatFile != "" || rules.FindTMDBIDInName(workDisplayName(g)) == "" {
		return "", false
	}
	parts := strings.Split(filepath.ToSlash(g.relKey), "/")
	if len(parts) < 2 {
		return "", false
	}
	for i := len(parts) - 2; i >= 0; i-- {
		switch strings.ToLower(strings.TrimSpace(parts[i])) {
		case "电影", "影片", "movie", "movies", "film", "films":
			return MediaTypeMovie, true
		case "剧集", "电视剧", "电视节目", "动漫", "动画", "tv", "shows", "series", "anime":
			return MediaTypeTV, true
		}
	}
	return "", false
}

func isLikelyMovieWorkFolder(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || rules.IsSeasonDirName(name) {
		return false
	}
	if rules.IsStandaloneMovieDirName(name) {
		return true
	}
	if rules.IsSpecialContentDirName(name) {
		return false
	}
	if rules.LooksLikeWorkDirName(name) {
		return true
	}
	parsed := rules.NormalizeParsedMedia(rules.ParseDirName(name))
	return parsed.Year != nil && strings.TrimSpace(parsed.Title) != ""
}

func isStructuralWorkSubdir(dir string) bool {
	name := filepath.Base(dir)
	if rules.IsSeasonDirName(name) {
		return true
	}
	if !rules.IsSpecialContentDirName(name) {
		return false
	}
	if !rules.IsStandaloneMovieDirName(name) {
		return true
	}
	if rules.FindTMDBIDInName(name) != "" {
		return false
	}
	return hasTVParentEvidence(filepath.Dir(dir), dir)
}

func hasTVParentEvidence(parentDir, currentDir string) bool {
	if fileExists(filepath.Join(parentDir, "tvshow.nfo")) {
		return true
	}
	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		path := filepath.Join(parentDir, entry.Name())
		if entry.IsDir() {
			if !sameFilePath(path, currentDir) && rules.IsSeasonDirName(entry.Name()) {
				return true
			}
			continue
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".strm") {
			stem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			if explicitSeasonEpisodeFileRe.MatchString(stem) {
				return true
			}
		}
	}
	return false
}

func workDisplayName(g workGroup) string {
	if g.flatFile != "" {
		return strings.TrimSuffix(filepath.Base(g.flatFile), filepath.Ext(g.flatFile))
	}
	return filepath.Base(g.absDir)
}

func findWorkByID(root, id string) (workGroup, error) {
	entries, err := scanStrmFiles(root)
	if err != nil {
		return workGroup{}, err
	}
	for _, g := range groupWorks(root, entries) {
		if pathToItemID(g.relKey) == id {
			return g, nil
		}
	}
	return workGroup{}, domain.Errorf(domain.CodeNotFound, "条目不存在")
}

func scanWorks(root string) ([]workGroup, error) {
	entries, err := scanStrmFiles(root)
	if err != nil {
		return nil, err
	}
	return groupWorks(root, entries), nil
}

func sameFilePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return aa == bb
}
