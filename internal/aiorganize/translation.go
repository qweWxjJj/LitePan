package aiorganize

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"litepan/internal/domain"
	"litepan/internal/mediaorganize/recognition"
)

const metadataTranslationSystemPrompt = `你是影视元数据本地化助手。请把输入中的演员名、导演名、编剧名和角色名转换为简体中文。
人物姓名优先使用通行的中文译名，没有通行译名时做准确音译；角色名结合影片标题和媒体类型翻译或音译。
不要改变人物身份，不要补充输入中不存在的内容。只返回严格 JSON：
{"items":[{"id":"原始 id","translated":"简体中文"}]}
每个 id 最多返回一次；无法可靠转换的项可以省略。不要解释，不要 Markdown。`

const metadataTranslationRepairPrompt = `将下面内容修正为严格 JSON。顶层只能有 items 数组，每项只能有 id 和 translated；删除重复或无法确认的项。不要解释，不要 Markdown。`

type MetadataTranslationItem struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type MetadataTranslationRequest struct {
	Title          string                    `json:"title,omitempty"`
	MediaType      string                    `json:"media_type,omitempty"`
	TargetLanguage string                    `json:"target_language"`
	Items          []MetadataTranslationItem `json:"items"`
}

type MetadataTranslationResult struct {
	ID         string `json:"id"`
	Translated string `json:"translated"`
}

// TranslateMetadata 使用当前启用的 AI 模型批量本地化一部作品的演职员元数据。
// 允许模型省略无法可靠翻译的项目，调用方应对缺失项保留原文。
func (s *Service) TranslateMetadata(ctx context.Context, req MetadataTranslationRequest) ([]MetadataTranslationResult, error) {
	if !s.Available() {
		return nil, recognition.ErrUnavailable
	}
	clean, ids, err := normalizeMetadataTranslationRequest(req)
	if err != nil {
		return nil, err
	}
	if len(clean.Items) == 0 {
		return []MetadataTranslationResult{}, nil
	}
	payload, _ := json.Marshal(clean)
	cfg := s.runtimeConfig()
	raw, err := s.chat(ctx, cfg, []chatMessage{
		{Role: "system", Content: metadataTranslationSystemPrompt},
		{Role: "user", Content: string(payload)},
	})
	if err != nil {
		return nil, err
	}
	items, err := parseMetadataTranslationResponse(raw, ids)
	if err == nil {
		return items, nil
	}
	repaired, repairErr := s.chat(ctx, cfg, []chatMessage{
		{Role: "system", Content: metadataTranslationRepairPrompt},
		{Role: "user", Content: raw},
	})
	if repairErr != nil {
		return nil, repairErr
	}
	items, err = parseMetadataTranslationResponse(repaired, ids)
	if err != nil {
		return nil, domain.Errorf(domain.CodeDriverError, "模型返回的元数据翻译格式不正确")
	}
	return items, nil
}

func normalizeMetadataTranslationRequest(req MetadataTranslationRequest) (MetadataTranslationRequest, map[string]struct{}, error) {
	out := MetadataTranslationRequest{
		Title:          strings.TrimSpace(req.Title),
		MediaType:      strings.TrimSpace(req.MediaType),
		TargetLanguage: strings.TrimSpace(req.TargetLanguage),
		Items:          make([]MetadataTranslationItem, 0, len(req.Items)),
	}
	if out.TargetLanguage == "" {
		out.TargetLanguage = "zh-CN"
	}
	ids := make(map[string]struct{}, len(req.Items))
	for _, item := range req.Items {
		item.ID = strings.TrimSpace(item.ID)
		item.Kind = strings.TrimSpace(item.Kind)
		item.Text = strings.TrimSpace(item.Text)
		if item.ID == "" || item.Text == "" {
			continue
		}
		if _, exists := ids[item.ID]; exists {
			return MetadataTranslationRequest{}, nil, errors.New("duplicate metadata translation id")
		}
		ids[item.ID] = struct{}{}
		out.Items = append(out.Items, item)
	}
	return out, ids, nil
}

func parseMetadataTranslationResponse(raw string, allowed map[string]struct{}) ([]MetadataTranslationResult, error) {
	var response struct {
		Items []MetadataTranslationResult `json:"items"`
	}
	if err := decodeJSONObject(raw, &response); err != nil {
		return nil, err
	}
	if response.Items == nil {
		return nil, errors.New("missing items")
	}
	seen := make(map[string]struct{}, len(response.Items))
	out := make([]MetadataTranslationResult, 0, len(response.Items))
	for _, item := range response.Items {
		item.ID = strings.TrimSpace(item.ID)
		item.Translated = strings.TrimSpace(item.Translated)
		if _, ok := allowed[item.ID]; !ok {
			return nil, errors.New("unknown metadata translation id")
		}
		if _, exists := seen[item.ID]; exists {
			return nil, errors.New("duplicate metadata translation result")
		}
		seen[item.ID] = struct{}{}
		if invalidPersonValue(item.Translated) {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func invalidPersonValue(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || strings.EqualFold(value, "nil") || strings.EqualFold(value, "<nil>") || strings.EqualFold(value, "null")
}
