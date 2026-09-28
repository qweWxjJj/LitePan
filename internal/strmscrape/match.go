package strmscrape

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"litepan/internal/mediaorganize/rules"
	"litepan/internal/mediaorganize/tmdb"
)

type tmdbInfo struct {
	TMDBID        string
	Title         string
	Original      string
	Year          *int
	Plot          string
	PosterPath    string
	BackdropPath  string
	Actors        []tmdbActor
	Directors     []tmdbPerson
	Writers       []tmdbPerson
	LogoPath      string
	MediaType     string
	Doubt         bool
	EpisodeCount  int // 默认全剧集数；刮削时会按本地已有季收窄
	Rating        float64
	VoteCount     int
	OriginalLang  string
	ReleaseDate   string
	EndDate       string
	Status        string
	IMDBID        string
	TVDBID        string
	Genres        []string
	Studios       []string
	Countries     []string
	Collection    *tmdbCollection
	CreditsLoaded bool
}

type tmdbCollection struct {
	TMDBID string
	Name   string
}

type tmdbActor struct {
	TMDBID      string
	Name        string
	Role        string
	ProfilePath string
	Order       int
}

type tmdbPerson struct {
	TMDBID      string
	Name        string
	ProfilePath string
}

func (s *Service) matchWork(ctx context.Context, client *tmdb.Client, g workGroup) (*tmdbInfo, error) {
	withActors := s.GetSettings().Actors
	mediaType := inferMediaType(g)
	folderName := workDisplayName(g)
	dirParsed := rules.NormalizeParsedMedia(rules.ParseDirName(folderName))

	// 对整理成功的目录，分类路径和目录 TMDB ID 是权威身份。禁止再搜索
	// 标题或跨 movie/tv 类型回退，避免相同数字 ID 命中另一命名空间。
	if organizedType, ok := organizedMediaType(g); ok {
		id := rules.FindTMDBIDInName(folderName)
		if id == "" {
			return nil, fmt.Errorf("已整理目录缺少 TMDB ID")
		}
		return lookupTMDBInfoExact(ctx, client, id, organizedType, withActors)
	}

	var fileParses []rules.ParsedMedia
	for _, e := range g.entries {
		stem := strings.TrimSuffix(filepath.Base(e.absPath), filepath.Ext(e.absPath))
		fileParses = append(fileParses, rules.NormalizeParsedMedia(rules.ParseFilenameStrict(stem+".mkv")))
	}
	if meta, ok := readWorkNFOMeta(g, mediaType); ok && strings.TrimSpace(meta.TMDBID) != "" {
		if info, err := lookupTMDBInfo(ctx, client, meta.TMDBID, mediaType, withActors); err == nil {
			return info, nil
		}
	}

	if id := rules.FindTMDBIDInName(folderName); id != "" {
		if info, err := lookupTMDBInfo(ctx, client, id, mediaType, withActors); err == nil {
			return info, nil
		}
	}
	for _, e := range g.entries {
		stem := strings.TrimSuffix(filepath.Base(e.absPath), filepath.Ext(e.absPath))
		if id := rules.FindTMDBIDInName(stem); id != "" {
			if info, err := lookupTMDBInfo(ctx, client, id, mediaType, withActors); err == nil {
				return info, nil
			}
		}
	}

	title := strings.TrimSpace(dirParsed.Title)
	year := dirParsed.Year
	if title == "" {
		for _, p := range fileParses {
			if strings.TrimSpace(p.Title) != "" {
				title = strings.TrimSpace(p.Title)
				if year == nil {
					year = p.Year
				}
				break
			}
		}
	}
	if title == "" {
		title = folderName
	}
	if title == "" {
		return nil, fmt.Errorf("无法解析标题")
	}

	info, err := searchTMDBInfo(ctx, client, title, year, mediaType, withActors)
	if err != nil && mediaType == MediaTypeTV {
		// 误判成剧集时回退电影搜索
		info, err = searchTMDBInfo(ctx, client, title, year, MediaTypeMovie, withActors)
	}
	if err != nil {
		return nil, err
	}
	if info.EpisodeCount == 0 && info.MediaType == MediaTypeTV {
		if raw, lerr := client.Lookup(ctx, info.TMDBID, MediaTypeTV); lerr == nil {
			if full, derr := decodeTMDBInfo(raw, MediaTypeTV); derr == nil && full.EpisodeCount > 0 {
				info.EpisodeCount = full.EpisodeCount
			}
		}
	}
	return info, nil
}

func lookupTMDBInfo(ctx context.Context, client *tmdb.Client, id, mediaType string, withActors bool) (*tmdbInfo, error) {
	order := []string{mediaType}
	if mediaType == MediaTypeTV {
		order = append(order, MediaTypeMovie)
	} else {
		order = append(order, MediaTypeTV)
	}
	var lastErr error
	for _, mt := range order {
		raw, err := client.LookupWithAppend(ctx, id, mt, actorAppendResponse(mt, withActors)...)
		if err != nil {
			lastErr = err
			continue
		}
		info, derr := decodeTMDBInfo(raw, mt)
		if derr != nil {
			lastErr = derr
			continue
		}
		return &info, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("TMDB 查询失败")
	}
	return nil, lastErr
}

func lookupTMDBInfoExact(ctx context.Context, client *tmdb.Client, id, mediaType string, withActors bool) (*tmdbInfo, error) {
	raw, err := client.LookupWithAppend(ctx, id, mediaType, actorAppendResponse(mediaType, withActors)...)
	if err != nil {
		return nil, err
	}
	info, err := decodeTMDBInfo(raw, mediaType)
	if err != nil {
		return nil, err
	}
	return &info, nil
}

func searchTMDBInfo(ctx context.Context, client *tmdb.Client, title string, year *int, mediaType string, withActors bool) (*tmdbInfo, error) {
	results, err := client.Search(ctx, title, year, mediaType)
	if err != nil {
		return nil, err
	}
	var best map[string]any
	doubt := false
	if year == nil {
		best, doubt = pickTMDBScrapeMatch(rules.RawJSONListToMaps(results), nil, mediaType, title)
	} else {
		// 带年份的第一次查询只接受完全相等；±1 年必须在不限年份的完整候选中判断唯一性。
		best = rules.PickTMDBSearchMatchForYear(rules.RawJSONListToMaps(results), year, mediaType, title)
	}
	if best == nil && year != nil {
		results, err = client.Search(ctx, title, nil, mediaType)
		if err != nil {
			return nil, err
		}
		best, doubt = pickTMDBScrapeMatch(rules.RawJSONListToMaps(results), year, mediaType, title)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("无搜索结果")
	}
	if best == nil {
		if year != nil {
			return nil, fmt.Errorf("没有标题相符且年份为 %d 或唯一相邻年份的结果", *year)
		}
		return nil, fmt.Errorf("没有标题相符的结果")
	}
	searchInfo, err := decodeTMDBInfo(mustRaw(best), mediaType)
	if err != nil {
		return nil, err
	}
	// 命中后统一读取详情，确保评分等详情元数据能稳定写入 NFO。
	detailRaw, err := client.LookupWithAppend(ctx, searchInfo.TMDBID, mediaType, actorAppendResponse(mediaType, withActors)...)
	if err != nil {
		return nil, fmt.Errorf("获取 TMDB 详情：%w", err)
	}
	info, err := decodeTMDBInfo(detailRaw, mediaType)
	if err != nil {
		return nil, err
	}
	info.Doubt = doubt
	return &info, nil
}

func actorAppendResponse(mediaType string, enabled bool) []string {
	out := []string{"external_ids"}
	if !enabled {
		return out
	}
	if mediaType == MediaTypeTV {
		return append(out, "aggregate_credits")
	}
	return append(out, "credits")
}

func pickTMDBScrapeMatch(results []map[string]any, year *int, mediaType, title string) (map[string]any, bool) {
	if best := rules.PickTMDBSearchMatchForYear(results, year, mediaType, title); best != nil {
		return best, year == nil && len(results) > 1
	}
	if best := rules.PickUniqueTMDBAdjacentYearMatch(results, year, mediaType, title); best != nil {
		return best, true
	}
	return nil, false
}

func (s *Service) writeMatched(ctx context.Context, client *tmdb.Client, g workGroup, info tmdbInfo, overwrite bool) error {
	_, err := s.writeMatchedOpts(ctx, client, g, info, overwrite, s.GetSettings().EpisodeInfo)
	return err
}

func (s *Service) writeMatchedOpts(ctx context.Context, client *tmdb.Client, g workGroup, info tmdbInfo, overwrite, withTVExtras bool) (epTMDB int, err error) {
	cfg := s.GetSettings()
	mediaType := info.MediaType
	if mediaType == "" {
		mediaType = inferMediaType(g)
		info.MediaType = mediaType
	}
	_, currentPoster := workMetaPaths(g, mediaType)
	needImages := overwrite || !fileExists(currentPoster) ||
		(cfg.Fanart && !workHasFanart(g)) || (cfg.ClearLogo && !workHasClearLogo(g))
	if cfg.Actors || needImages {
		info, err = enrichTMDBExtras(ctx, client, info, cfg, needImages)
		if err != nil {
			return 0, err
		}
	}
	epTMDB = 0
	if withTVExtras {
		epTMDB = info.EpisodeCount
	}
	epLocal, _ := countTVEpisodeProgress(g)
	if err := writePendingState(g, scrapeState{
		Status:  PendingRunning,
		EpLocal: epLocal,
		EpTMDB:  epTMDB,
	}); err != nil {
		return 0, err
	}
	needTVExtras := withTVExtras && mediaType == MediaTypeTV && g.flatFile == "" && strings.TrimSpace(info.TMDBID) != ""
	nfo, poster := workMetaPaths(g, mediaType)
	actors := buildNFOActors(client, info.Actors)
	peopleSkipped := false
	// 目标 NFO 不存在或不是标准 NFO（如压制组信息文件）都重写；后者直接覆盖。
	if nfoWriteNeeded(overwrite, nfo) {
		if mediaType == MediaTypeTV {
			if err := writeWorkNFOFromTMDB(nfo, "tvshow", info, actors); err != nil {
				return 0, err
			}
		} else if err := writeWorkNFOFromTMDB(nfo, "movie", info, actors); err != nil {
			return 0, err
		}
	} else if cfg.Actors {
		// 补写演职员是可选项：NFO 结构异常时只警告跳过，不能中断整部作品（否则海报/背景图/Logo 也写不成）。
		if err := appendNFOPeople(nfo, actors, info.Directors, info.Writers); err != nil {
			peopleSkipped = true
			if s.log != nil {
				s.log.Warn("STRM 刮削补写演职员信息失败，已跳过", "nfo", nfo, "err", err)
			}
		}
	}
	if (overwrite || !fileExists(poster)) && strings.TrimSpace(info.PosterPath) != "" {
		data, err := client.DownloadImage(ctx, info.PosterPath, artworkDownloadSize)
		if err != nil {
			return 0, err
		}
		if err := writeImageFile(poster, data); err != nil {
			return 0, err
		}
	}
	if cfg.Fanart && (overwrite || !workHasFanart(g)) && strings.TrimSpace(info.BackdropPath) != "" {
		if _, err := s.writeOptionalArtwork(ctx, client, info.BackdropPath, workFanartPath(g), "详情页背景图"); err != nil {
			return 0, err
		}
	}
	if cfg.ClearLogo && (overwrite || !workHasClearLogo(g)) && strings.TrimSpace(info.LogoPath) != "" {
		if _, err := s.writeOptionalArtwork(ctx, client, info.LogoPath, workClearLogoPath(g), "影片 Logo"); err != nil {
			return 0, err
		}
	}
	if withTVExtras && needTVExtras {
		if n, err := s.writeTVExtras(ctx, client, g, info, overwrite); err != nil {
			return epTMDB, fmt.Errorf("补写季/集元数据失败：%w", err)
		} else if n > 0 {
			epTMDB = n
		}
	}
	// 异步补季/集时由调用方 finalize；此处同步路径直接收尾
	if !withTVExtras {
		if info.Doubt {
			_ = writePendingState(g, scrapeState{Status: PendingDoubt})
		} else {
			clearPendingMarker(g)
		}
	} else if withTVExtras || !needTVExtras {
		finalizeAfterScrape(g, mediaType, epTMDB, info.Doubt)
	}
	// 记录本次 TMDB 是否根本没有可选资源，避免下一轮再次为同一部作品发请求。
	syncOptionalAssetState(g, cfg, info, peopleSkipped)
	clearManualComplete(g)
	return epTMDB, nil
}

func enrichTMDBExtras(ctx context.Context, client *tmdb.Client, info tmdbInfo, cfg Settings, needImages bool) (tmdbInfo, error) {
	var payload map[string]any
	if cfg.Actors && !info.CreditsLoaded {
		appendTo := []string{}
		if info.MediaType == MediaTypeTV {
			appendTo = append(appendTo, "aggregate_credits")
		} else {
			appendTo = append(appendTo, "credits")
		}
		raw, err := client.LookupWithAppend(ctx, info.TMDBID, info.MediaType, appendTo...)
		if err != nil {
			return info, fmt.Errorf("获取 TMDB 扩展信息：%w", err)
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return info, fmt.Errorf("解析 TMDB 扩展信息：%w", err)
		}
		if cfg.Fanart {
			info.BackdropPath = strings.TrimSpace(anyString(payload["backdrop_path"]))
		}
		if language := strings.TrimSpace(anyString(payload["original_language"])); language != "" {
			info.OriginalLang = language
		}
		creditsKey := "credits"
		if info.MediaType == MediaTypeTV {
			creditsKey = "aggregate_credits"
		}
		info.Actors = decodeTMDBActors(payload[creditsKey], 0)
		info.Directors, info.Writers = decodeTMDBCrew(payload[creditsKey])
	}
	// 一次 images 请求同时完成原语言海报、无文字背景和 Logo 选择，避免分别请求。
	if needImages && strings.TrimSpace(info.TMDBID) != "" {
		images, imageErr := client.FetchImages(ctx, info.TMDBID, info.MediaType)
		if imageErr != nil {
			// 图片列表是原语言优选的增强信息。失败时沿用详情接口返回的图片，
			// 不让一项可选资源阻断 NFO 和已有图片的生成。
			return info, nil
		}
		info.PosterPath = selectTMDBArtwork(images, "posters", info.OriginalLang, false, info.PosterPath)
		if cfg.Fanart {
			info.BackdropPath = selectTMDBArtwork(images, "backdrops", "", false, info.BackdropPath)
		}
		if cfg.ClearLogo {
			info.LogoPath = selectTMDBLogo(images, info.OriginalLang)
		}
	}
	return info, nil
}

func selectTMDBLogo(raw json.RawMessage, original string) string {
	return selectTMDBArtwork(raw, "logos", original, true, "")
}

type tmdbArtworkCandidate struct {
	FilePath    string  `json:"file_path"`
	Language    *string `json:"iso_639_1"`
	VoteAverage float64 `json:"vote_average"`
	VoteCount   int     `json:"vote_count"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
}

// selectTMDBArtwork 先按语言语义选组，再在组内按有效投票、评分和像素面积排序。
// 原语言优先；无文字图片其次；英文再次；zh 仅在作品原语言为中文或没有其他图片时使用。
func selectTMDBArtwork(raw json.RawMessage, kind, original string, pngOnly bool, fallback string) string {
	var payload map[string]json.RawMessage
	if json.Unmarshal(raw, &payload) != nil {
		return strings.TrimSpace(fallback)
	}
	var candidates []tmdbArtworkCandidate
	if json.Unmarshal(payload[kind], &candidates) != nil {
		return strings.TrimSpace(fallback)
	}
	normalizeLanguage := func(value string) string {
		value = strings.ToLower(strings.TrimSpace(value))
		if i := strings.IndexAny(value, "-_"); i >= 0 {
			value = value[:i]
		}
		return value
	}
	original = normalizeLanguage(original)
	languageRank := func(candidate tmdbArtworkCandidate) int {
		actual := ""
		if candidate.Language != nil {
			actual = normalizeLanguage(*candidate.Language)
		}
		switch {
		case original != "" && actual == original:
			return 0
		case actual == "":
			return 1
		case actual == "en":
			return 2
		case actual == "zh":
			return 4
		default:
			return 3
		}
	}
	filtered := candidates[:0]
	for _, candidate := range candidates {
		candidate.FilePath = strings.TrimSpace(candidate.FilePath)
		if candidate.FilePath == "" || (pngOnly && !strings.EqualFold(filepath.Ext(candidate.FilePath), ".png")) {
			continue
		}
		filtered = append(filtered, candidate)
	}
	if len(filtered) == 0 {
		return strings.TrimSpace(fallback)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		leftRank, rightRank := languageRank(filtered[i]), languageRank(filtered[j])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if filtered[i].VoteCount != filtered[j].VoteCount {
			return filtered[i].VoteCount > filtered[j].VoteCount
		}
		if filtered[i].VoteAverage != filtered[j].VoteAverage {
			return filtered[i].VoteAverage > filtered[j].VoteAverage
		}
		return filtered[i].Width*filtered[i].Height > filtered[j].Width*filtered[j].Height
	})
	return filtered[0].FilePath
}

func decodeTMDBActors(raw any, limit int) []tmdbActor {
	credits, _ := raw.(map[string]any)
	cast, _ := credits["cast"].([]any)
	if limit <= 0 || limit > len(cast) {
		limit = len(cast)
	}
	out := make([]tmdbActor, 0, limit)
	for _, value := range cast {
		item, _ := value.(map[string]any)
		name := cleanPersonText(item["name"])
		profilePath := cleanPersonText(item["profile_path"])
		// 演员数量通常较多；TMDB 没有头像的演员不写入 NFO，避免空白人物卡片。
		if name == "" || profilePath == "" {
			continue
		}
		role := cleanPersonText(item["character"])
		if role == "" {
			if roles, _ := item["roles"].([]any); len(roles) > 0 {
				if first, _ := roles[0].(map[string]any); first != nil {
					role = cleanPersonText(first["character"])
				}
			}
		}
		order := len(out)
		if parsed := asInt(item["order"]); parsed != nil {
			order = *parsed
		}
		out = append(out, tmdbActor{
			TMDBID:      personTMDBID(item),
			Name:        name,
			Role:        role,
			ProfilePath: profilePath,
			Order:       order,
		})
		if len(out) == limit {
			break
		}
	}
	return out
}

func decodeTMDBCrew(raw any) (directors, writers []tmdbPerson) {
	credits, _ := raw.(map[string]any)
	crew, _ := credits["crew"].([]any)
	directorSeen := map[string]struct{}{}
	writerSeen := map[string]struct{}{}
	for _, value := range crew {
		item, _ := value.(map[string]any)
		name := cleanPersonText(item["name"])
		if name == "" {
			continue
		}
		department := strings.ToLower(cleanPersonText(item["department"]))
		jobs := []string{cleanPersonText(item["job"])}
		if aggregateJobs, _ := item["jobs"].([]any); len(aggregateJobs) > 0 {
			jobs = jobs[:0]
			for _, rawJob := range aggregateJobs {
				job, _ := rawJob.(map[string]any)
				jobs = append(jobs, cleanPersonText(job["job"]))
			}
		}
		isDirector := false
		isWriter := department == "writing"
		for _, job := range jobs {
			switch strings.ToLower(strings.TrimSpace(job)) {
			case "director", "series director":
				isDirector = true
			case "writer", "screenplay", "story", "teleplay", "adaptation", "novel":
				isWriter = true
			}
		}
		person := tmdbPerson{TMDBID: personTMDBID(item), Name: name, ProfilePath: cleanPersonText(item["profile_path"])}
		if isDirector {
			directors = appendUniqueTMDBPerson(directors, directorSeen, person)
		}
		if isWriter {
			writers = appendUniqueTMDBPerson(writers, writerSeen, person)
		}
	}
	return directors, writers
}

func cleanPersonText(value any) string {
	text := strings.TrimSpace(anyString(value))
	if strings.EqualFold(text, "<nil>") || strings.EqualFold(text, "nil") || strings.EqualFold(text, "null") {
		return ""
	}
	return text
}

func personTMDBID(item map[string]any) string {
	if id := asInt(item["id"]); id != nil && *id > 0 {
		return strconv.Itoa(*id)
	}
	return ""
}

func appendUniqueTMDBPerson(values []tmdbPerson, seen map[string]struct{}, person tmdbPerson) []tmdbPerson {
	key := "name:" + strings.ToLower(strings.TrimSpace(person.Name))
	if key == "" {
		return values
	}
	if _, ok := seen[key]; ok {
		return values
	}
	seen[key] = struct{}{}
	if person.TMDBID != "" {
		seen["id:"+strings.TrimSpace(person.TMDBID)] = struct{}{}
	}
	person.Name = strings.TrimSpace(person.Name)
	return append(values, person)
}

func buildNFOActors(client *tmdb.Client, actors []tmdbActor) []nfoActor {
	out := make([]nfoActor, 0, len(actors))
	for _, actor := range actors {
		if strings.TrimSpace(actor.ProfilePath) == "" {
			continue
		}
		out = append(out, nfoActor{
			Name: actor.Name, Role: actor.Role, Type: "Actor", TMDBID: actor.TMDBID,
			Order: actor.Order, Thumb: client.ImageURL(actor.ProfilePath, artworkDownloadSize),
		})
	}
	return out
}

// tmdbEpisodeCountForLocalSeasons 按 finale 截断正片季，避免跨季绝对集号被误当总集数。
func tmdbEpisodeCountForLocalSeasons(ctx context.Context, client *tmdb.Client, g workGroup, tmdbID string) (int, error) {
	seasons := listLocalRegularSeasonNumbers(g)
	if client == nil || strings.TrimSpace(tmdbID) == "" || len(seasons) == 0 {
		return 0, fmt.Errorf("无本地正片季")
	}
	rawSeasons, err := client.FetchTVSeasons(ctx, tmdbID)
	if err != nil {
		return 0, err
	}
	fallback := tmdbSeasonEpisodeCountMap(rawSeasons)
	total := 0
	for _, sn := range seasons {
		n := 0
		if detail, derr := fetchSeasonDetail(ctx, client, tmdbID, sn); derr == nil {
			n = effectiveSeasonEpisodeCount(detail, fallback[sn])
		} else {
			n = fallback[sn]
		}
		if n > 0 {
			total += n
		}
	}
	if total <= 0 {
		return 0, fmt.Errorf("本地季在 TMDB 无集数")
	}
	return total, nil
}

func tmdbSeasonEpisodeCountMap(rawSeasons []json.RawMessage) map[int]int {
	out := map[int]int{}
	for _, raw := range rawSeasons {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		num := asInt(m["season_number"])
		ep := asInt(m["episode_count"])
		if num == nil || ep == nil || *num <= 0 || *ep <= 0 {
			continue
		}
		out[*num] = *ep
	}
	return out
}

// effectiveSeasonEpisodeCount 有 finale 时按集列表计数，否则保留 episode_count。
func effectiveSeasonEpisodeCount(detail *tmdbSeasonDetail, fallback int) int {
	fin := finaleEpisodeNumber(detail)
	if fin <= 0 {
		return fallback
	}
	n := 0
	for _, ep := range detail.Episodes {
		if ep.EpisodeNumber > 0 && ep.EpisodeNumber <= fin {
			n++
		}
	}
	if n > 0 {
		return n
	}
	return fallback
}

func finaleEpisodeNumber(detail *tmdbSeasonDetail) int {
	if detail == nil {
		return 0
	}
	best := 0
	for _, ep := range detail.Episodes {
		if ep.EpisodeType != "finale" || ep.EpisodeNumber <= 0 {
			continue
		}
		if ep.EpisodeNumber > best {
			best = ep.EpisodeNumber
		}
	}
	return best
}

func (s *Service) writeSeasonPosters(ctx context.Context, client *tmdb.Client, g workGroup, tmdbID string, overwrite bool, seasonDirs []seasonDir) error {
	showDir := g.absDir
	seasons := seasonNumbersFromDirs(seasonDirs)
	if len(seasons) == 0 {
		return nil
	}
	rawSeasons, err := client.FetchTVSeasons(ctx, tmdbID)
	if err != nil {
		return err
	}
	byNum := map[int]string{}
	for _, raw := range rawSeasons {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		num := asInt(m["season_number"])
		if num == nil {
			continue
		}
		poster := strings.TrimSpace(anyString(m["poster_path"]))
		if poster == "" {
			continue
		}
		byNum[*num] = poster
	}
	for _, season := range seasons {
		posterPath := byNum[season]
		if posterPath == "" {
			continue
		}
		out := seasonPosterPath(showDir, season)
		if !overwrite && fileExists(out) {
			continue
		}
		if _, err := s.writeOptionalArtwork(ctx, client, posterPath, out, fmt.Sprintf("第 %d 季海报", season)); err != nil {
			return err
		}
	}
	return nil
}

func asInt(v any) *int {
	switch t := v.(type) {
	case float64:
		n := int(t)
		return &n
	case int:
		return &t
	case int64:
		n := int(t)
		return &n
	case json.Number:
		i, err := t.Int64()
		if err != nil {
			return nil
		}
		n := int(i)
		return &n
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return nil
		}
		return &i
	default:
		return nil
	}
}

func decodeTMDBInfo(raw json.RawMessage, mediaType string) (tmdbInfo, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return tmdbInfo{}, err
	}
	id, title, original, year := rules.ExtractTMDBDisplayFields(m, mediaType)
	plot := strings.TrimSpace(anyString(m["overview"]))
	poster := strings.TrimSpace(anyString(m["poster_path"]))
	backdrop := strings.TrimSpace(anyString(m["backdrop_path"]))
	if id == "" || title == "" {
		return tmdbInfo{}, fmt.Errorf("TMDB 结果缺少标题")
	}
	epCount := 0
	if n := asInt(m["number_of_episodes"]); n != nil && *n > 0 {
		epCount = *n
	}
	creditsKey := "credits"
	studioSource := m["production_companies"]
	if mediaType == MediaTypeTV {
		creditsKey = "aggregate_credits"
		studioSource = m["networks"]
	}
	_, creditsLoaded := m[creditsKey]
	info := tmdbInfo{
		TMDBID:        id,
		Title:         title,
		Original:      original,
		Year:          year,
		Plot:          plot,
		PosterPath:    poster,
		BackdropPath:  backdrop,
		MediaType:     mediaType,
		EpisodeCount:  epCount,
		Rating:        anyFloat64(m["vote_average"]),
		VoteCount:     intValue(m["vote_count"]),
		OriginalLang:  strings.TrimSpace(anyString(m["original_language"])),
		ReleaseDate:   firstNonEmpty(anyString(m["release_date"]), anyString(m["first_air_date"])),
		EndDate:       strings.TrimSpace(anyString(m["last_air_date"])),
		Status:        strings.TrimSpace(anyString(m["status"])),
		IMDBID:        firstNonEmpty(anyString(m["imdb_id"]), nestedString(m["external_ids"], "imdb_id")),
		TVDBID:        nestedIDString(m["external_ids"], "tvdb_id"),
		Genres:        decodeNamedValues(m["genres"]),
		Studios:       decodeNamedValues(studioSource),
		Countries:     decodeCountryValues(m["production_countries"]),
		Actors:        decodeTMDBActors(m[creditsKey], 0),
		Directors:     nil,
		Writers:       nil,
		CreditsLoaded: creditsLoaded,
	}
	if mediaType == MediaTypeMovie {
		if collection, ok := m["belongs_to_collection"].(map[string]any); ok {
			info.Collection = &tmdbCollection{TMDBID: valueIDString(collection["id"]), Name: strings.TrimSpace(anyString(collection["name"]))}
			if info.Collection.TMDBID == "" || info.Collection.Name == "" {
				info.Collection = nil
			}
		}
	}
	info.Directors, info.Writers = decodeTMDBCrew(m[creditsKey])
	return info, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func nestedString(value any, key string) string {
	m, _ := value.(map[string]any)
	return strings.TrimSpace(anyString(m[key]))
}

func nestedIDString(value any, key string) string {
	m, _ := value.(map[string]any)
	return valueIDString(m[key])
}

func valueIDString(value any) string {
	if id := asInt(value); id != nil && *id > 0 {
		return strconv.Itoa(*id)
	}
	return strings.TrimSpace(anyString(value))
}

func decodeNamedValues(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if name := strings.TrimSpace(anyString(item["name"])); name != "" {
			out = append(out, name)
		}
	}
	return cleanPersonNames(out)
}

func decodeCountryValues(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if name := firstNonEmpty(anyString(item["name"]), anyString(item["iso_3166_1"])); name != "" {
			out = append(out, name)
		}
	}
	return cleanPersonNames(out)
}

func anyFloat64(v any) float64 {
	switch value := v.(type) {
	case float64:
		return value
	case json.Number:
		n, _ := value.Float64()
		return n
	case string:
		n, _ := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return n
	default:
		return 0
	}
}

func intValue(v any) int {
	if n := asInt(v); n != nil {
		return *n
	}
	return 0
}

func mustRaw(m map[string]any) json.RawMessage {
	b, _ := json.Marshal(m)
	return b
}

func (s *Service) newTMDBClient() *tmdb.Client {
	cfg := s.GetSettings()
	apiKey := strings.TrimSpace(cfg.TmdbAPIKey)
	if apiKey == "" {
		return nil
	}
	proxy := tmdb.BuildProxyURL(tmdb.ProxyConfig{
		Enabled:  cfg.ProxyEnabled,
		URL:      cfg.ProxyURL,
		Username: cfg.ProxyUsername,
		Password: cfg.ProxyPassword,
	})
	return tmdb.NewClient(tmdb.Options{
		APIKey:         apiKey,
		Language:       cfg.TmdbLanguage,
		ProxyURL:       proxy,
		Timeout:        20 * time.Second,
		MaxRetries:     2,
		RetryBaseDelay: time.Second,
		APIBaseHost:    cfg.TmdbAPIHost,
		ImageBaseHost:  cfg.TmdbImageHost,
	})
}
