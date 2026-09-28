package strmscrape

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type workNFO struct {
	XMLName       xml.Name
	Plot          string      `xml:"plot,omitempty"`
	Title         string      `xml:"title"`
	OriginalTitle string      `xml:"originaltitle,omitempty"`
	SortTitle     string      `xml:"sorttitle,omitempty"`
	Year          string      `xml:"year,omitempty"`
	TMDBID        string      `xml:"tmdbid,omitempty"`
	IMDBID        string      `xml:"imdbid,omitempty"`
	TVDBID        string      `xml:"tvdbid,omitempty"`
	Premiered     string      `xml:"premiered,omitempty"`
	ReleaseDate   string      `xml:"releasedate,omitempty"`
	EndDate       string      `xml:"enddate,omitempty"`
	Status        string      `xml:"status,omitempty"`
	Ratings       *nfoRatings `xml:"ratings,omitempty"`
	Actors        []nfoActor  `xml:"actor,omitempty"`
	// Emby 使用 director 表示导演，使用 credits 表示编剧；两者均为可重复元素。
	Directors []nfoPersonText `xml:"director,omitempty"`
	Writers   []nfoPersonText `xml:"writer,omitempty"`
	Credits   []nfoPersonText `xml:"credits,omitempty"`
	Countries []string        `xml:"country,omitempty"`
	Genres    []string        `xml:"genre,omitempty"`
	Studios   []string        `xml:"studio,omitempty"`
	Set       *nfoSet         `xml:"set,omitempty"`
	UniqueIDs []nfoUniqueID   `xml:"uniqueid,omitempty"`
}

type nfoSet struct {
	TMDBCollectionID string `xml:"tmdbcolid,attr"`
	Name             string `xml:"name"`
}

type nfoUniqueID struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type nfoRatings struct {
	Rating nfoRating `xml:"rating"`
}

type nfoRating struct {
	Name    string `xml:"name,attr"`
	Max     string `xml:"max,attr"`
	Default string `xml:"default,attr"`
	Value   string `xml:"value"`
	Votes   int    `xml:"votes"`
}

type nfoActor struct {
	XMLName xml.Name `xml:"actor"`
	Name    string   `xml:"name"`
	Role    string   `xml:"role,omitempty"`
	Type    string   `xml:"type,omitempty"`
	TMDBID  string   `xml:"tmdbid,omitempty"`
	Order   int      `xml:"order"`
	Thumb   string   `xml:"thumb,omitempty"`
}

type seasonNFO struct {
	XMLName      xml.Name `xml:"season"`
	Title        string   `xml:"title,omitempty"`
	SeasonNumber string   `xml:"seasonnumber"`
	Plot         string   `xml:"plot,omitempty"`
	Premiered    string   `xml:"premiered,omitempty"`
}

type episodeNFO struct {
	XMLName   xml.Name        `xml:"episodedetails"`
	Title     string          `xml:"title"`
	Season    string          `xml:"season"`
	Episode   string          `xml:"episode"`
	Plot      string          `xml:"plot,omitempty"`
	Aired     string          `xml:"aired,omitempty"`
	TMDBID    string          `xml:"tmdbid,omitempty"`
	ShowTitle string          `xml:"showtitle,omitempty"`
	Ratings   *nfoRatings     `xml:"ratings,omitempty"`
	Actors    []nfoActor      `xml:"actor,omitempty"`
	Directors []nfoPersonText `xml:"director,omitempty"`
	Writers   []nfoPersonText `xml:"writer,omitempty"`
	Credits   []nfoPersonText `xml:"credits,omitempty"`
}

type nfoPersonText struct {
	XMLName xml.Name
	TMDBID  string `xml:"tmdbid,attr,omitempty"`
	Value   string `xml:",chardata"`
}

var nfoRootCloseRe = regexp.MustCompile(`(?i)</(?:movie|tvshow|episodedetails)\s*>`)

// nfoLooksStandard 文件含 movie/tvshow 根节点才算可用 NFO，压制组的 MediaInfo 文本不算。
func nfoLooksStandard(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(data))
	return strings.Contains(lower, "<movie") || strings.Contains(lower, "<tvshow")
}

// nfoWriteNeeded 目标 NFO 不存在或不是标准 NFO 时都要重写，压制组文本对 Kodi/Emby 无用。
func nfoWriteNeeded(overwrite bool, nfo string) bool {
	return overwrite || !nfoLooksStandard(nfo)
}

// workMetaPaths 返回电影或剧集的兼容元数据路径。
func workMetaPaths(g workGroup, mediaType string) (nfoPath, posterPath string) {
	if mediaType == MediaTypeTV && g.flatFile == "" {
		return filepath.Join(g.absDir, "tvshow.nfo"), filepath.Join(g.absDir, "poster.jpg")
	}
	stemPath := primaryStrmStem(g)
	if stemPath == "" {
		return filepath.Join(g.absDir, "movie.nfo"), filepath.Join(g.absDir, "poster.jpg")
	}
	nfoPath = stemPath + ".nfo"
	if g.flatFile != "" {
		return nfoPath, stemPath + "-poster.jpg"
	}
	return nfoPath, filepath.Join(g.absDir, "poster.jpg")
}

func primaryStrmStem(g workGroup) string {
	if g.flatFile != "" {
		return strings.TrimSuffix(g.flatFile, filepath.Ext(g.flatFile))
	}
	if len(g.entries) == 0 {
		return ""
	}
	return strings.TrimSuffix(g.entries[0].absPath, filepath.Ext(g.entries[0].absPath))
}

func workHasNFO(g workGroup, mediaType string) bool {
	for _, p := range workNFOCandidates(g, mediaType) {
		if nfoLooksStandard(p) {
			return true
		}
	}
	return false
}

func workHasActors(g workGroup, mediaType string) bool {
	for _, path := range workNFOCandidates(g, mediaType) {
		if nfoLooksStandard(path) && nfoHasActors(path) {
			return true
		}
	}
	return false
}

func workHasDirectors(g workGroup, mediaType string) bool {
	for _, path := range workNFOCandidates(g, mediaType) {
		if nfoLooksStandard(path) && nfoHasDirectors(path) {
			return true
		}
	}
	return false
}

func workHasWriters(g workGroup, mediaType string) bool {
	for _, path := range workNFOCandidates(g, mediaType) {
		if nfoLooksStandard(path) && nfoHasWriters(path) {
			return true
		}
	}
	return false
}

func workHasPoster(g workGroup, mediaType string) bool {
	for _, p := range workPosterCandidates(g, mediaType) {
		if fileExists(p) {
			return true
		}
	}
	return false
}

func workNFOCandidates(g workGroup, mediaType string) []string {
	if mediaType == MediaTypeTV && g.flatFile == "" {
		return []string{filepath.Join(g.absDir, "tvshow.nfo")}
	}
	out := make([]string, 0, len(g.entries)+2)
	if g.flatFile != "" {
		stem := strings.TrimSuffix(g.flatFile, filepath.Ext(g.flatFile))
		return []string{stem + ".nfo"}
	}
	for _, e := range g.entries {
		stem := strings.TrimSuffix(e.absPath, filepath.Ext(e.absPath))
		out = append(out, stem+".nfo")
	}
	// 兼容上一版误写的 movie.nfo
	out = append(out, filepath.Join(g.absDir, "movie.nfo"))
	return out
}

func workPosterCandidates(g workGroup, mediaType string) []string {
	_ = mediaType
	if g.flatFile != "" {
		stem := strings.TrimSuffix(g.flatFile, filepath.Ext(g.flatFile))
		return []string{stem + "-poster.jpg", stem + ".jpg"}
	}
	out := []string{
		filepath.Join(g.absDir, "poster.jpg"),
		filepath.Join(g.absDir, "folder.jpg"),
		filepath.Join(g.absDir, "cover.jpg"),
	}
	for _, e := range g.entries {
		stem := strings.TrimSuffix(e.absPath, filepath.Ext(e.absPath))
		out = append(out, stem+"-poster.jpg", stem+".jpg")
	}
	return out
}

func workPosterFile(g workGroup, mediaType string) string {
	for _, p := range workPosterCandidates(g, mediaType) {
		if fileExists(p) {
			return p
		}
	}
	_, poster := workMetaPaths(g, mediaType)
	return poster
}

func workFanartPath(g workGroup) string {
	if g.flatFile != "" {
		return primaryStrmStem(g) + "-fanart.jpg"
	}
	return filepath.Join(g.absDir, "fanart.jpg")
}

func workHasFanart(g workGroup) bool {
	if g.flatFile != "" {
		stem := primaryStrmStem(g)
		return fileExists(stem+"-fanart.jpg") || fileExists(stem+"-fanart.png")
	}
	for _, name := range []string{"fanart.jpg", "fanart.png", "backdrop.jpg", "backdrop.png", "background.jpg", "background.png"} {
		if fileExists(filepath.Join(g.absDir, name)) {
			return true
		}
	}
	return false
}

func workClearLogoPath(g workGroup) string {
	if g.flatFile != "" {
		return primaryStrmStem(g) + "-clearlogo.png"
	}
	return filepath.Join(g.absDir, "clearlogo.png")
}

func workHasClearLogo(g workGroup) bool {
	if g.flatFile != "" {
		return fileExists(primaryStrmStem(g) + "-clearlogo.png")
	}
	return fileExists(filepath.Join(g.absDir, "clearlogo.png"))
}

func seasonPosterPath(showDir string, season int) string {
	if season <= 0 {
		return filepath.Join(showDir, "season-specials-poster.jpg")
	}
	return filepath.Join(showDir, fmt.Sprintf("season%02d-poster.jpg", season))
}

func listLocalSeasonNumbers(showDir string) []int {
	return seasonNumbersFromDirs(listLocalSeasonDirs(showDir))
}

func seasonNumbersFromDirs(dirs []seasonDir) []int {
	seen := map[int]struct{}{}
	var out []int
	for _, dir := range dirs {
		if _, ok := seen[dir.number]; ok {
			continue
		}
		seen[dir.number] = struct{}{}
		out = append(out, dir.number)
	}
	return out
}

func writeMovieNFO(path, title, tmdbID, plot string, year *int, actors ...nfoActor) error {
	return writeWorkNFO(path, "movie", title, tmdbID, plot, year, actors)
}

func writeTVShowNFO(path, title, tmdbID, plot string, year *int, actors ...nfoActor) error {
	return writeWorkNFO(path, "tvshow", title, tmdbID, plot, year, actors)
}

func writeWorkNFO(path, root, title, tmdbID, plot string, year *int, actors []nfoActor) error {
	return writeWorkNFOWithRating(path, root, title, tmdbID, plot, year, actors, nil, nil, 0, 0)
}

func writeWorkNFOWithRating(path, root, title, tmdbID, plot string, year *int, actors []nfoActor, directors, writers []tmdbPerson, rating float64, votes int) error {
	nfo := workNFO{
		XMLName:   xml.Name{Local: root},
		Title:     strings.TrimSpace(title),
		TMDBID:    strings.TrimSpace(tmdbID),
		Plot:      strings.TrimSpace(plot),
		Actors:    actors,
		Directors: nfoPersonNodes("director", directors),
		Writers:   nfoPersonNodes("writer", writers),
		Credits:   nfoPersonNodes("credits", writers),
	}
	if rating > 0 {
		nfo.Ratings = &nfoRatings{Rating: nfoRating{
			Name:    "themoviedb",
			Max:     "10",
			Default: "true",
			Value:   strconv.FormatFloat(rating, 'f', -1, 64),
			Votes:   votes,
		}}
	}
	if year != nil && *year > 0 {
		nfo.Year = fmt.Sprintf("%d", *year)
	}
	return writeXML(path, nfo)
}

func writeWorkNFOFromTMDB(path, root string, info tmdbInfo, actors []nfoActor) error {
	nfo := workNFO{
		XMLName:       xml.Name{Local: root},
		Plot:          strings.TrimSpace(info.Plot),
		Title:         strings.TrimSpace(info.Title),
		OriginalTitle: strings.TrimSpace(info.Original),
		SortTitle:     strings.TrimSpace(info.Title),
		TMDBID:        strings.TrimSpace(info.TMDBID),
		IMDBID:        strings.TrimSpace(info.IMDBID),
		TVDBID:        strings.TrimSpace(info.TVDBID),
		Premiered:     strings.TrimSpace(info.ReleaseDate),
		ReleaseDate:   strings.TrimSpace(info.ReleaseDate),
		EndDate:       strings.TrimSpace(info.EndDate),
		Status:        strings.TrimSpace(info.Status),
		Actors:        actors,
		Directors:     nfoPersonNodes("director", info.Directors),
		Writers:       nfoPersonNodes("writer", info.Writers),
		Credits:       nfoPersonNodes("credits", info.Writers),
		Countries:     cleanPersonNames(info.Countries),
		Genres:        cleanPersonNames(info.Genres),
		Studios:       cleanPersonNames(info.Studios),
	}
	if info.Year != nil && *info.Year > 0 {
		nfo.Year = strconv.Itoa(*info.Year)
	}
	if info.Rating > 0 {
		nfo.Ratings = &nfoRatings{Rating: nfoRating{Name: "themoviedb", Max: "10", Default: "true", Value: strconv.FormatFloat(info.Rating, 'f', -1, 64), Votes: info.VoteCount}}
	}
	if info.Collection != nil {
		nfo.Set = &nfoSet{TMDBCollectionID: strings.TrimSpace(info.Collection.TMDBID), Name: strings.TrimSpace(info.Collection.Name)}
	}
	for _, id := range []nfoUniqueID{{Type: "tmdb", Value: info.TMDBID}, {Type: "imdb", Value: info.IMDBID}, {Type: "tvdb", Value: info.TVDBID}} {
		if id.Value = strings.TrimSpace(id.Value); id.Value != "" {
			nfo.UniqueIDs = append(nfo.UniqueIDs, id)
		}
	}
	return writeXML(path, nfo)
}

func nfoHasActors(path string) bool {
	return nfoHasElement(path, "actor")
}

func nfoHasDirectors(path string) bool { return nfoHasElement(path, "director") }
func nfoHasWriters(path string) bool {
	return nfoHasElement(path, "writer") || nfoHasElement(path, "credits")
}

func nfoHasElement(path, name string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pattern := `(?i)<\s*` + regexp.QuoteMeta(strings.TrimSpace(name)) + `(?:\s|>)`
	return regexp.MustCompile(pattern).Match(data)
}

// appendNFOActors 在仅补缺模式下保留已有 NFO 的全部内容，只补入演员节点。
func appendNFOActors(path string, actors []nfoActor) error {
	return appendNFOPeople(path, actors, nil, nil)
}

// appendNFOPeople 在仅补缺模式下保留已有 NFO 的全部内容，只补入当前缺失的
// 演员、导演和编剧节点。神医助手同时写 writer 与 credits，保持相同兼容格式。
func appendNFOPeople(path string, actors []nfoActor, directors, writers []tmdbPerson) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	matches := nfoRootCloseRe.FindAllIndex(data, -1)
	if len(matches) == 0 {
		return fmt.Errorf("NFO 缺少 movie/tvshow 根节点")
	}
	idx := matches[len(matches)-1][0]
	var fragments strings.Builder
	if !nfoHasActors(path) {
		for _, actor := range actors {
			raw, err := xml.Marshal(actor)
			if err != nil {
				return err
			}
			fragments.WriteString("  ")
			fragments.Write(raw)
			fragments.WriteByte('\n')
		}
	}
	appendTextNodes := func(element string, values []tmdbPerson) error {
		for _, value := range nfoPersonNodes(element, values) {
			raw, marshalErr := xml.Marshal(value)
			if marshalErr != nil {
				return marshalErr
			}
			fragments.WriteString("  ")
			fragments.Write(raw)
			fragments.WriteByte('\n')
		}
		return nil
	}
	if !nfoHasDirectors(path) {
		if err := appendTextNodes("director", directors); err != nil {
			return err
		}
	}
	if !nfoHasElement(path, "writer") {
		if err := appendTextNodes("writer", writers); err != nil {
			return err
		}
	}
	if !nfoHasElement(path, "credits") {
		if err := appendTextNodes("credits", writers); err != nil {
			return err
		}
	}
	if fragments.Len() == 0 {
		return nil
	}
	updated := append([]byte{}, data[:idx]...)
	updated = append(updated, []byte(fragments.String())...)
	updated = append(updated, data[idx:]...)
	return os.WriteFile(path, updated, 0o644)
}

func cleanPersonNames(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, raw := range values {
		name := strings.TrimSpace(raw)
		if name == "" || strings.EqualFold(name, "<nil>") || strings.EqualFold(name, "nil") || strings.EqualFold(name, "null") {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	return out
}

func nfoPersonNodes(element string, values []tmdbPerson) []nfoPersonText {
	out := make([]nfoPersonText, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, person := range values {
		name := strings.TrimSpace(person.Name)
		if name == "" || strings.EqualFold(name, "<nil>") || strings.EqualFold(name, "nil") || strings.EqualFold(name, "null") {
			continue
		}
		id := strings.TrimSpace(person.TMDBID)
		key := "name:" + strings.ToLower(name)
		if id != "" {
			key = "id:" + id
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, nfoPersonText{XMLName: xml.Name{Local: element}, TMDBID: id, Value: name})
	}
	return out
}

func writeSeasonNFO(path string, season int, title, plot, premiered string) error {
	nfo := seasonNFO{
		Title:        strings.TrimSpace(title),
		SeasonNumber: fmt.Sprintf("%d", season),
		Plot:         strings.TrimSpace(plot),
		Premiered:    strings.TrimSpace(premiered),
	}
	return writeXML(path, nfo)
}

func writeEpisodeNFO(path, title, showTitle, plot, aired, tmdbID string, season, episode int, rating float64, votes int, actors []nfoActor, directors, writers []tmdbPerson) error {
	nfo := episodeNFO{
		Title:     strings.TrimSpace(title),
		Season:    fmt.Sprintf("%d", season),
		Episode:   fmt.Sprintf("%d", episode),
		Plot:      strings.TrimSpace(plot),
		Aired:     strings.TrimSpace(aired),
		TMDBID:    strings.TrimSpace(tmdbID),
		ShowTitle: strings.TrimSpace(showTitle),
		Actors:    actors,
		Directors: nfoPersonNodes("director", directors),
		Writers:   nfoPersonNodes("writer", writers),
		Credits:   nfoPersonNodes("credits", writers),
	}
	if rating > 0 {
		nfo.Ratings = &nfoRatings{Rating: nfoRating{
			Name:    "themoviedb",
			Max:     "10",
			Default: "true",
			Value:   strconv.FormatFloat(rating, 'f', -1, 64),
			Votes:   votes,
		}}
	}
	return writeXML(path, nfo)
}

func writeXML(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	body := append([]byte(xml.Header), data...)
	body = append(body, '\n')
	return os.WriteFile(path, body, 0o644)
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func writeImageFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
