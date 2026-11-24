package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	gateway "mioreel-gateway/server"
	"mioreel-gateway/server/internal/cache"
	"mioreel-gateway/server/internal/device"
	"mioreel-gateway/server/internal/profile"
	"mioreel-gateway/server/internal/resolver"
	"mioreel-gateway/server/internal/signer"
	"mioreel-gateway/server/internal/upstream"
)

var categoryAliases = map[string]string{
	"":             "playlet", // default per docs/02-client-api.md §4: category default real-drama -> playlet
	"real-drama":   "playlet",
	"playlet":      "playlet",
	"comic-drama":  "motion_comic",
	"comic":        "motion_comic",
	"motion_comic": "motion_comic",
	"ai-drama":     "ai_playlet",
	"ai_playlet":   "ai_playlet",
	"all":          "all",
	"short_play":   "all",
}

type discoveryTabCache struct {
	cell      *rawCellView
	allSelect bool
	updatedAt time.Time
}

type genreCursor struct {
	CellID        string
	Offset        int
	SessionID     string
	RankVersion   string
	SelectedItems string
}

type genrePageState struct {
	cursors    map[int]genreCursor
	pages      map[int]catalogPageResult
	endReached bool
	lastPage   int
	updated    time.Time
}

type catalogPageResult struct {
	list    []gateway.SeriesSummary
	hasMore bool
}

// CatalogService implements GET /api/v1/catalog using Hongguo discovery APIs.
type CatalogService struct {
	profiles *profile.Manager
	devices  *device.Manager
	upstream *upstream.HongguoClient
	pageTTL  *cache.TTLCache[gateway.CatalogData]

	mu         sync.Mutex
	tabCache   *discoveryTabCache
	genreState map[string]*genrePageState
}

func NewCatalogService(profiles *profile.Manager, devices *device.Manager, up *upstream.HongguoClient) *CatalogService {
	return &CatalogService{
		profiles:   profiles,
		devices:    devices,
		upstream:   up,
		pageTTL:    cache.NewTTLCache[gateway.CatalogData](256),
		genreState: make(map[string]*genrePageState),
	}
}

func (s *CatalogService) GetCatalog(ctx context.Context, rawCategory string, page, limit int) (gateway.CatalogData, error) {
	genre, ok := categoryAliases[strings.TrimSpace(rawCategory)]
	if !ok {
		return gateway.CatalogData{}, NewBadRequest("category 仅支持 real-drama / comic-drama / ai-drama / all")
	}
	if page < 1 {
		return gateway.CatalogData{}, NewBadRequest("page 必须大于等于 1")
	}
	if limit <= 0 {
		limit = 40
	}
	if limit > 100 {
		limit = 100
	}

	cacheKey := fmt.Sprintf("%s:%d:%d", genre, page, limit)
	if cached, ok := s.pageTTL.Get(cacheKey); ok {
		return cached, nil
	}

	deadline := time.Now().Add(15 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}

	dev, err := s.devices.Get(ctx, deadline)
	if err != nil {
		return gateway.CatalogData{}, MapError(err)
	}

	res, err := s.loadPage(ctx, dev, genre, page, deadline)
	if err != nil {
		return gateway.CatalogData{}, MapError(err)
	}

	list := res.list
	if len(list) > limit {
		list = list[:limit]
	}

	pageCount := page
	if res.hasMore && len(list) > 0 {
		pageCount = 99
	}

	out := gateway.CatalogData{
		Page:      page,
		PageCount: pageCount,
		Total:     len(list),
		List:      list,
	}
	s.pageTTL.Set(cacheKey, out, 5*time.Minute)
	return out, nil
}

type rawCellView struct {
	CellIDStr    string           `json:"cell_id_str"`
	NextOffset   resolver.FlexInt `json:"next_offset"`
	HasMore      *bool            `json:"has_more"`
	SessionID    string           `json:"session_id"`
	RankVersion  string           `json:"rank_version"`
	VideoData    []rawVideoCard   `json:"video_data"`
	CellData     []*rawCellView   `json:"cell_data"`
	CellSelector *struct {
		OuterRow *struct {
			Items []struct {
				SelectorItemID string `json:"selector_item_id"`
				ShowName       string `json:"show_name"`
				IsSelected     bool   `json:"is_selected"`
			} `json:"items"`
		} `json:"outer_row"`
	} `json:"cell_selector"`
}

type rawVideoCard struct {
	SeriesIDStr string           `json:"series_id_str"`
	SeriesID    string           `json:"series_id"`
	Title       string           `json:"title"`
	SubTitle    string           `json:"sub_title"`
	Cover       string           `json:"cover"`
	VideoDesc   string           `json:"video_desc"`
	EpisodeCnt  resolver.FlexInt `json:"episode_cnt"`
	DisablePlay bool             `json:"disable_play"`
	Tags        []string         `json:"tags"`
	Category    string           `json:"category"`
}

func (s *CatalogService) getDiscoveryTab(ctx context.Context, dev signer.Device, deadline time.Time) (*discoveryTabCache, error) {
	s.mu.Lock()
	tc := s.tabCache
	s.mu.Unlock()
	if tc != nil && time.Since(tc.updatedAt) < 30*time.Minute {
		return tc, nil
	}

	raw, err := s.upstream.FetchDiscoveryRaw(ctx, s.profiles.Current(), dev, "/reading/bookapi/bookmall/tab/v", []signer.QueryKV{
		{Key: "tab_type", Value: "8"},
		{Key: "tab_index", Value: "0"},
		{Key: "offset", Value: "0"},
	}, deadline)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Data struct {
			TabItem []struct {
				TabType  resolver.FlexInt `json:"tab_type"`
				CellData []*rawCellView   `json:"cell_data"`
			} `json:"tab_item"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, &upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "discovery tab parse failed", Err: err}
	}

	var targetCell *rawCellView
	for _, tab := range parsed.Data.TabItem {
		if int(tab.TabType) == 8 {
			for _, c := range tab.CellData {
				if c != nil && c.CellIDStr != "" && len(c.CellData) >= 0 {
					targetCell = c
					break
				}
			}
		}
	}
	if targetCell == nil {
		return nil, &upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "discovery tab missing cell_view"}
	}

	allSelected := false
	if targetCell.CellSelector != nil && targetCell.CellSelector.OuterRow != nil {
		for _, item := range targetCell.CellSelector.OuterRow.Items {
			if item.SelectorItemID == "all" && item.IsSelected {
				allSelected = true
				break
			}
		}
	}

	tc = &discoveryTabCache{
		cell:      targetCell,
		allSelect: allSelected,
		updatedAt: time.Now(),
	}
	s.mu.Lock()
	s.tabCache = tc
	s.mu.Unlock()
	return tc, nil
}

func (s *CatalogService) loadPage(ctx context.Context, dev signer.Device, genre string, page int, deadline time.Time) (catalogPageResult, error) {
	s.mu.Lock()
	st := s.genreState[genre]
	if page == 1 {
		st = &genrePageState{
			cursors: make(map[int]genreCursor),
			pages:   make(map[int]catalogPageResult),
			updated: time.Now(),
		}
		s.genreState[genre] = st
	} else {
		if st == nil || time.Since(st.updated) > 2*time.Hour {
			s.mu.Unlock()
			return catalogPageResult{}, NewBadRequest("分类翻页游标已过期，请从第 1 页重新翻页")
		}
		if res, ok := st.pages[page]; ok {
			s.mu.Unlock()
			return res, nil
		}
		if st.endReached && page > st.lastPage {
			s.mu.Unlock()
			return catalogPageResult{list: []gateway.SeriesSummary{}, hasMore: false}, nil
		}
	}
	var cur genreCursor
	hasCursor := false
	if page > 1 {
		cur, hasCursor = st.cursors[page]
		if !hasCursor {
			s.mu.Unlock()
			return catalogPageResult{}, NewBadRequest("分类翻页游标不存在，请从第 1 页顺序翻页")
		}
	}
	s.mu.Unlock()

	if page == 1 {
		res1, nextCur, err := s.fetchFirstPage(ctx, dev, genre, deadline)
		if err != nil {
			return catalogPageResult{}, err
		}
		s.mu.Lock()
		st.pages[1] = res1
		if res1.hasMore {
			st.cursors[2] = nextCur
		} else {
			st.endReached = true
			st.lastPage = 1
		}
		st.updated = time.Now()
		s.mu.Unlock()
		return res1, nil
	}

	res, nextCur, err := s.fetchNextPage(ctx, dev, cur, deadline)
	if err != nil {
		return catalogPageResult{}, err
	}
	s.mu.Lock()
	st.pages[page] = res
	if res.hasMore {
		st.cursors[page+1] = nextCur
	} else {
		st.endReached = true
		st.lastPage = page
	}
	st.updated = time.Now()
	s.mu.Unlock()
	return res, nil
}

func (s *CatalogService) fetchFirstPage(ctx context.Context, dev signer.Device, genre string, deadline time.Time) (catalogPageResult, genreCursor, error) {
	tab, err := s.getDiscoveryTab(ctx, dev, deadline)
	if err != nil {
		return catalogPageResult{}, genreCursor{}, err
	}

	if genre == "all" && tab.allSelect {
		list := extractVideos(tab.cell)
		nextOff := int(tab.cell.NextOffset)
		hasMore := (tab.cell.HasMore == nil || *tab.cell.HasMore) && nextOff > 0 && len(list) > 0
		return catalogPageResult{list: list, hasMore: hasMore}, genreCursor{
			CellID:        tab.cell.CellIDStr,
			Offset:        nextOff,
			SessionID:     tab.cell.SessionID,
			RankVersion:   tab.cell.RankVersion,
			SelectedItems: genre,
		}, nil
	}

	cur := genreCursor{
		CellID:        tab.cell.CellIDStr,
		Offset:        0,
		SelectedItems: genre,
	}
	return s.callCellChange(ctx, dev, cur, 2, deadline)
}

func (s *CatalogService) fetchNextPage(ctx context.Context, dev signer.Device, cur genreCursor, deadline time.Time) (catalogPageResult, genreCursor, error) {
	return s.callCellChange(ctx, dev, cur, 1, deadline)
}

func (s *CatalogService) callCellChange(ctx context.Context, dev signer.Device, cur genreCursor, selectorChangeType int, deadline time.Time) (catalogPageResult, genreCursor, error) {
	params := []signer.QueryKV{
		{Key: "cell_id", Value: cur.CellID},
		{Key: "tab_type", Value: "8"},
		{Key: "selected_items", Value: cur.SelectedItems},
		{Key: "sub_selected_items", Value: ""},
		{Key: "unlimited_selector_change_type", Value: strconv.Itoa(selectorChangeType)},
		{Key: "client_template", Value: "2"},
		{Key: "client_req_type", Value: "2"},
		{Key: "change_type", Value: "1"},
		{Key: "offset", Value: strconv.Itoa(cur.Offset)},
		{Key: "limit", Value: "40"},
	}
	if cur.SessionID != "" {
		params = append(params, signer.QueryKV{Key: "session_id", Value: cur.SessionID})
	}
	if cur.RankVersion != "" {
		params = append(params, signer.QueryKV{Key: "rank_version", Value: cur.RankVersion})
	}

	raw, err := s.upstream.FetchDiscoveryRaw(ctx, s.profiles.Current(), dev, "/reading/bookapi/bookmall/cell/change/v", params, deadline)
	if err != nil {
		return catalogPageResult{}, genreCursor{}, err
	}

	var parsed struct {
		Data struct {
			CellView    *rawCellView     `json:"cell_view"`
			NextOffset  resolver.FlexInt `json:"next_offset"`
			HasMore     *bool            `json:"has_more"`
			SessionID   string           `json:"session_id"`
			RankVersion string           `json:"rank_version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Data.CellView == nil {
		return catalogPageResult{}, genreCursor{}, &upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "cell_change missing cell_view", Err: err}
	}

	cv := parsed.Data.CellView
	list := extractVideos(cv)
	nextOff := int(parsed.Data.NextOffset)
	if nextOff == 0 {
		nextOff = int(cv.NextOffset)
	}
	dataMore := parsed.Data.HasMore == nil || *parsed.Data.HasMore
	cellMore := cv.HasMore == nil || *cv.HasMore
	hasMore := dataMore && cellMore && nextOff > cur.Offset && len(list) > 0

	nextCur := genreCursor{
		CellID:        firstNonEmpty(cv.CellIDStr, cur.CellID),
		Offset:        nextOff,
		SessionID:     firstNonEmpty(parsed.Data.SessionID, cv.SessionID),
		RankVersion:   firstNonEmpty(parsed.Data.RankVersion, cv.RankVersion),
		SelectedItems: cur.SelectedItems,
	}
	return catalogPageResult{list: list, hasMore: hasMore}, nextCur, nil
}

func extractVideos(root *rawCellView) []gateway.SeriesSummary {
	var out []gateway.SeriesSummary
	seen := make(map[string]struct{})
	var visit func(c *rawCellView)
	visit = func(c *rawCellView) {
		if c == nil {
			return
		}
		for _, v := range c.VideoData {
			id := strings.TrimSpace(firstNonEmpty(v.SeriesIDStr, v.SeriesID))
			title := cleanText(v.Title)
			if !isValidID(id) || title == "" || v.DisablePlay {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			epCnt := int(v.EpisodeCnt)
			remark := ""
			if epCnt > 0 {
				remark = fmt.Sprintf("全%d集", epCnt)
			}
			tags := v.Tags
			if len(tags) == 0 && strings.TrimSpace(v.SubTitle) != "" {
				tags = []string{strings.TrimSpace(v.SubTitle)}
			}
			if tags == nil {
				tags = []string{}
			}
			out = append(out, gateway.SeriesSummary{
				SeriesID:     id,
				Title:        title,
				Cover:        cleanCoverURL(v.Cover),
				EpisodeCount: epCnt,
				Remark:       remark,
				Intro:        cleanText(v.VideoDesc),
				Tags:         tags,
				Year:         "",
			})
		}
		for _, child := range c.CellData {
			visit(child)
		}
	}
	visit(root)
	if out == nil {
		out = []gateway.SeriesSummary{}
	}
	return out
}
