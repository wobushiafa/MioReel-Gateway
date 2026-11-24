package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	gateway "mioreel-gateway/server"
	"mioreel-gateway/server/internal/cache"
	"mioreel-gateway/server/internal/device"
	"mioreel-gateway/server/internal/profile"
	"mioreel-gateway/server/internal/upstream"
)

// SeriesService resolves series metadata and episode tables with 15-minute caching.
type SeriesService struct {
	profiles *profile.Manager
	devices  *device.Manager
	upstream *upstream.HongguoClient
	cache    *cache.TTLCache[gateway.SeriesData]
}

func NewSeriesService(profiles *profile.Manager, devices *device.Manager, up *upstream.HongguoClient) *SeriesService {
	return &SeriesService{
		profiles: profiles,
		devices:  devices,
		upstream: up,
		cache:    cache.NewTTLCache[gateway.SeriesData](512),
	}
}

func (s *SeriesService) GetSeries(ctx context.Context, source, seriesID string) (gateway.SeriesData, error) {
	if source != "" && source != "hongguo" {
		return gateway.SeriesData{}, NewBadRequest("不支持的 source: " + source)
	}
	seriesID = strings.TrimSpace(seriesID)
	if !isValidID(seriesID) {
		return gateway.SeriesData{}, NewBadRequest("series_id 必须为 10~30 位数字")
	}

	if cached, ok := s.cache.Get(seriesID); ok {
		return cached, nil
	}

	deadline := time.Now().Add(12 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}

	dev, err := s.devices.Get(ctx, deadline)
	if err != nil {
		return gateway.SeriesData{}, MapError(err)
	}

	data, err := s.upstream.FetchDetailRaw(ctx, s.profiles.Current(), dev, seriesID, deadline)
	if err != nil || data == nil || len(data.VideoList) == 0 {
		// Fallback to web detail page if app API didn't yield episodes
		if webData, webErr := s.upstream.FetchDetailWebFallback(ctx, seriesID); webErr == nil && webData != nil && len(webData.VideoList) > 0 {
			data = webData
			err = nil
		}
	}
	if err != nil {
		return gateway.SeriesData{}, MapError(err)
	}
	if data == nil || len(data.VideoList) == 0 {
		return gateway.SeriesData{}, NewNotFound("剧集或分集不存在 (upstream=MISSING_EPISODE)", "MISSING_EPISODE")
	}

	episodes := make([]gateway.EpisodeInfo, 0, len(data.VideoList))
	for i, ep := range data.VideoList {
		vid := strings.TrimSpace(firstNonEmpty(ep.VID, ep.VideoID))
		if vid == "" {
			continue
		}
		epTitle := cleanText(ep.Title)
		if epTitle == "" {
			epTitle = fmt.Sprintf("第%d集", i+1)
		}
		episodes = append(episodes, gateway.EpisodeInfo{
			No:        i + 1,
			VID:       vid,
			Title:     epTitle,
			Available: !ep.DisablePlay,
		})
	}
	if len(episodes) == 0 {
		return gateway.SeriesData{}, NewNotFound("剧集或分集不存在 (upstream=MISSING_EPISODE)", "MISSING_EPISODE")
	}

	totalEp := int(data.EpisodeCnt)
	if totalEp < len(episodes) {
		totalEp = len(episodes)
	}
	accessibleEp := int(data.AccessibleEpisodes)
	if accessibleEp <= 0 {
		accessibleEp = int(data.AccessibleCount)
	}
	if accessibleEp <= 0 {
		accessibleEp = len(episodes)
	}

	out := gateway.SeriesData{
		SeriesID:           seriesID,
		Title:              cleanText(firstNonEmpty(data.SeriesTitle, data.SeriesName, data.Title)),
		Cover:              cleanCoverURL(firstNonEmpty(data.SeriesCover, data.Cover)),
		Intro:              cleanText(firstNonEmpty(data.SeriesIntro, data.VideoDesc, data.Intro, data.Desc)),
		TotalEpisodes:      totalEp,
		AccessibleEpisodes: accessibleEp,
		Episodes:           episodes,
	}
	s.cache.Set(seriesID, out, 15*time.Minute)
	return out, nil
}
