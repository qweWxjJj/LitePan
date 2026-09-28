package strmscrape

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"litepan/internal/aiorganize"

	_ "modernc.org/sqlite"
)

const peopleTranslationVersion = "v1"

type peopleTranslationTarget struct {
	id       string
	kind     string
	text     string
	cacheKey string
	apply    func(string)
}

func translationVersionFor(info tmdbInfo) string {
	if info.PeopleTranslationAttempted {
		return peopleTranslationVersion
	}
	return ""
}

func (s *Service) localizeTMDBPeople(ctx context.Context, info tmdbInfo) tmdbInfo {
	if s == nil || s.ai == nil || !s.ai.Available() {
		return info
	}
	info.PeopleTranslationAttempted = true
	targets := buildPeopleTranslationTargets(&info)
	if len(targets) == 0 {
		return info
	}

	cache, cacheErr := openPeopleTranslationCache(s.dataDir)
	if cacheErr != nil && s.log != nil {
		s.log.Warn("打开演职员翻译缓存失败，本次将直接调用 AI", "err", cacheErr)
	}
	if cache != nil {
		defer cache.Close()
	}

	pending := make([]peopleTranslationTarget, 0, len(targets))
	for _, target := range targets {
		if cache != nil {
			if translated, ok := cache.Get(ctx, target.cacheKey); ok && validChineseTranslation(translated) {
				target.apply(translated)
				if translated != target.text {
					info.PeopleLocalized = true
				}
				continue
			}
		}
		pending = append(pending, target)
	}
	if len(pending) == 0 {
		return info
	}

	request := aiorganize.MetadataTranslationRequest{
		Title:          info.Title,
		MediaType:      info.MediaType,
		TargetLanguage: "zh-CN",
		Items:          make([]aiorganize.MetadataTranslationItem, 0, len(pending)),
	}
	for _, target := range pending {
		request.Items = append(request.Items, aiorganize.MetadataTranslationItem{
			ID: target.id, Kind: target.kind, Text: target.text,
		})
	}
	translated, err := s.ai.TranslateMetadata(ctx, request)
	if err != nil {
		if s.log != nil {
			s.log.Warn("AI 演职员中文化失败，已保留 TMDB 原文", "title", info.Title, "tmdb_id", info.TMDBID, "err", err)
		}
		return info
	}
	byID := make(map[string]string, len(translated))
	for _, item := range translated {
		if validChineseTranslation(item.Translated) {
			byID[item.ID] = cleanPersonValue(item.Translated)
		}
	}
	cacheValues := make(map[string]string, len(byID))
	for _, target := range pending {
		value := byID[target.id]
		if value == "" {
			continue
		}
		target.apply(value)
		cacheValues[target.cacheKey] = value
		if value != target.text {
			info.PeopleLocalized = true
		}
	}
	if cache != nil && len(cacheValues) > 0 {
		if err := cache.Put(ctx, cacheValues); err != nil && s.log != nil {
			s.log.Warn("保存演职员翻译缓存失败", "err", err)
		}
	}
	return info
}

func buildPeopleTranslationTargets(info *tmdbInfo) []peopleTranslationTarget {
	if info == nil {
		return nil
	}
	targets := make([]peopleTranslationTarget, 0, len(info.Actors)*2+len(info.Directors)+len(info.Writers))
	add := func(id, kind, value string, apply func(string)) {
		value = cleanPersonValue(value)
		if !needsChineseLocalization(value) {
			return
		}
		targets = append(targets, peopleTranslationTarget{
			id:       id,
			kind:     kind,
			text:     value,
			cacheKey: peopleTranslationCacheKey(info.MediaType, info.TMDBID, kind, value),
			apply:    apply,
		})
	}
	for i := range info.Actors {
		index := i
		add(fmt.Sprintf("actor_name_%d", i), "actor_name", info.Actors[i].Name, func(value string) {
			info.Actors[index].Name = value
		})
		add(fmt.Sprintf("actor_role_%d", i), "character", info.Actors[i].Role, func(value string) {
			info.Actors[index].Role = value
		})
	}
	for i := range info.Directors {
		index := i
		add(fmt.Sprintf("director_%d", i), "director", info.Directors[i], func(value string) {
			info.Directors[index] = value
		})
	}
	for i := range info.Writers {
		index := i
		add(fmt.Sprintf("writer_%d", i), "writer", info.Writers[i], func(value string) {
			info.Writers[index] = value
		})
	}
	return targets
}

func cleanPersonValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "nil") || strings.EqualFold(value, "<nil>") || strings.EqualFold(value, "null") {
		return ""
	}
	return value
}

func needsChineseLocalization(value string) bool {
	value = cleanPersonValue(value)
	if value == "" {
		return false
	}
	for _, r := range value {
		if unicode.In(r, unicode.Latin, unicode.Cyrillic, unicode.Greek, unicode.Hangul, unicode.Hiragana, unicode.Katakana) {
			return true
		}
	}
	return false
}

func validChineseTranslation(value string) bool {
	value = cleanPersonValue(value)
	if value == "" {
		return false
	}
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func peopleTranslationCacheKey(mediaType, tmdbID, kind, source string) string {
	plain := strings.Join([]string{
		peopleTranslationVersion,
		strings.ToLower(strings.TrimSpace(mediaType)),
		strings.TrimSpace(tmdbID),
		strings.TrimSpace(kind),
		strings.TrimSpace(source),
	}, "\x00")
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

type peopleTranslationCache struct {
	db *sql.DB
}

func openPeopleTranslationCache(dataDir string) (*peopleTranslationCache, error) {
	path := filepath.Join(strings.TrimSpace(dataDir), "strmscrape", "people-translations.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(abs) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS people_translations (
  cache_key TEXT PRIMARY KEY,
  translated_text TEXT NOT NULL,
  updated_at INTEGER NOT NULL
)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &peopleTranslationCache{db: db}, nil
}

func (c *peopleTranslationCache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

func (c *peopleTranslationCache) Get(ctx context.Context, key string) (string, bool) {
	if c == nil || c.db == nil {
		return "", false
	}
	var value string
	if err := c.db.QueryRowContext(ctx, `SELECT translated_text FROM people_translations WHERE cache_key = ?`, key).Scan(&value); err != nil {
		return "", false
	}
	value = cleanPersonValue(value)
	return value, value != ""
}

func (c *peopleTranslationCache) Put(ctx context.Context, values map[string]string) error {
	if c == nil || c.db == nil || len(values) == 0 {
		return nil
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for key, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO people_translations(cache_key, translated_text, updated_at)
VALUES(?, ?, ?) ON CONFLICT(cache_key) DO UPDATE SET translated_text=excluded.translated_text, updated_at=excluded.updated_at`, key, value, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
