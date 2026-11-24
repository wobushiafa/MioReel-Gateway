package service

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"

	gateway "mioreel-gateway/server"
	"mioreel-gateway/server/internal/device"
	"mioreel-gateway/server/internal/profile"
	"mioreel-gateway/server/internal/resolver"
	"mioreel-gateway/server/internal/upstream"
)

var danmakuIDPattern = regexp.MustCompile(`^hongguo:(\d{10,30}):(\d{10,30})$`)

// DanmakuService implements GET /api/v1/danmaku.
type DanmakuService struct {
	profiles *profile.Manager
	devices  *device.Manager
	upstream *upstream.HongguoClient
}

func NewDanmakuService(profiles *profile.Manager, devices *device.Manager, up *upstream.HongguoClient) *DanmakuService {
	return &DanmakuService{
		profiles: profiles,
		devices:  devices,
		upstream: up,
	}
}

func (s *DanmakuService) GetDanmaku(ctx context.Context, danmakuID string, startMs, durationMs int64) (gateway.DanmakuData, error) {
	m := danmakuIDPattern.FindStringSubmatch(strings.TrimSpace(danmakuID))
	if len(m) != 3 {
		return gateway.DanmakuData{}, NewBadRequest("danmaku_id 格式必须为 hongguo:{series_id}:{vid}")
	}
	if durationMs <= 0 {
		durationMs = 300000
	}
	if startMs < 0 || startMs >= durationMs || durationMs > 86400000 {
		return gateway.DanmakuData{}, NewBadRequest("start_ms / duration_ms 范围无效")
	}

	nextMs := startMs + 30000
	if nextMs > durationMs {
		nextMs = durationMs
	}

	deadline := time.Now().Add(5 * time.Second)
	dev, err := s.devices.Get(ctx, deadline)
	if err != nil {
		return gateway.DanmakuData{}, MapError(err)
	}

	raw, err := s.upstream.FetchDanmakuRaw(ctx, s.profiles.Current(), dev, m[1], m[2], startMs, durationMs, "", deadline)
	if err != nil {
		return gateway.DanmakuData{}, MapError(err)
	}

	var parsed struct {
		Code resolver.FlexInt `json:"code"`
		Data struct {
			DataList []struct {
				Comment struct {
					CommentID string `json:"comment_id"`
					Common    struct {
						GroupID string `json:"group_id"`
						Content struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"common"`
					Expand struct {
						OffsetTime int64 `json:"offset_time"`
					} `json:"expand"`
				} `json:"comment"`
			} `json:"data_list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return gateway.DanmakuData{}, MapError(&upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "danmaku parse failed", Err: err})
	}
	if int(parsed.Code) != 0 {
		return gateway.DanmakuData{}, MapError(&upstream.UpstreamError{Code: "BUSINESS_REJECTED", Message: "danmaku rejected"})
	}

	seen := make(map[string]struct{})
	items := make([]gateway.DanmakuItem, 0, len(parsed.Data.DataList))
	for _, row := range parsed.Data.DataList {
		c := row.Comment
		if c.Common.GroupID != m[2] || c.CommentID == "" || len(c.CommentID) > 120 {
			continue
		}
		if _, ok := seen[c.CommentID]; ok {
			continue
		}
		if c.Expand.OffsetTime < startMs || c.Expand.OffsetTime >= nextMs {
			continue
		}
		txt := strings.TrimSpace(c.Common.Content.Text)
		if txt == "" {
			continue
		}
		runes := []rune(txt)
		if len(runes) > 180 {
			txt = string(runes[:180])
		}
		seen[c.CommentID] = struct{}{}
		items = append(items, gateway.DanmakuItem{
			ID:     c.CommentID,
			TimeMs: c.Expand.OffsetTime,
			Text:   txt,
			Color:  "#FFFFFF",
		})
		if len(items) >= 90 {
			break
		}
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].TimeMs < items[j].TimeMs
	})

	return gateway.DanmakuData{
		EpisodeID: danmakuID,
		StartMs:   startMs,
		NextMs:    nextMs,
		Items:     items,
	}, nil
}
