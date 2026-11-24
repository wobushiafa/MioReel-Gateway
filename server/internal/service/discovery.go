package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
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

var (
	rankingBoardIDPattern   = regexp.MustCompile(`^hongguo-app:(all|human|comic_series_rank|ai_playlet):([a-z0-9_]{1,80})$`)
	rankingBoardItemPattern = regexp.MustCompile(`^[a-z0-9_]{1,80}$`)
	allowedRankingGroups    = map[string]struct{}{
		"all":               {},
		"human":             {},
		"comic_series_rank": {},
		"ai_playlet":        {},
	}
)

type discoveryCursor struct {
	Params      []signer.QueryKV
	CellID      string
	Offset      int
	SessionID   string
	RankVersion string
}

type discoveryPageState struct {
	cursors    map[int]discoveryCursor
	pages      map[int]catalogPageResult
	endReached bool
	lastPage   int
	updated    time.Time
}

type discoveryCell struct {
	CellIDStr    string           `json:"cell_id_str"`
	NextOffset   resolver.FlexInt `json:"next_offset"`
	HasMore      *bool            `json:"has_more"`
	SessionID    string           `json:"session_id"`
	RankVersion  string           `json:"rank_version"`
	VideoData    []discoveryVideo `json:"video_data"`
	CellData     []*discoveryCell `json:"cell_data"`
	CellSelector *struct {
		OuterRow *struct {
			Items []struct {
				SelectorItemID  string `json:"selector_item_id"`
				ShowName        string `json:"show_name"`
				SubTitle        string `json:"sub_title"`
				IsSelected      bool   `json:"is_selected"`
				SubCellSelector *struct {
					OuterRow *struct {
						Items []struct {
							SelectorItemID string `json:"selector_item_id"`
							ShowName       string `json:"show_name"`
							SubTitle       string `json:"sub_title"`
							IsSelected     bool   `json:"is_selected"`
						} `json:"items"`
					} `json:"outer_row"`
				} `json:"sub_cell_selector"`
			} `json:"items"`
		} `json:"outer_row"`
	} `json:"cell_selector"`
}

type discoveryVideo struct {
	SeriesIDStr string           `json:"series_id_str"`
	SeriesID    string           `json:"series_id"`
	Title       string           `json:"title"`
	SubTitle    string           `json:"sub_title"`
	Cover       string           `json:"cover"`
	VideoDesc   string           `json:"video_desc"`
	EpisodeCnt  resolver.FlexInt `json:"episode_cnt"`
	DisablePlay bool             `json:"disable_play"`
	Tags        []string         `json:"tags"`
	VideoDetail *struct {
		SeriesIDStr string           `json:"series_id_str"`
		SeriesID    string           `json:"series_id"`
		SeriesTitle string           `json:"series_title"`
		SeriesCover string           `json:"series_cover"`
		SeriesIntro string           `json:"series_intro"`
		EpisodeCnt  resolver.FlexInt `json:"episode_cnt"`
	} `json:"video_detail"`
}

// DiscoveryService implements rankings, recommendations, and feed endpoints using Hongguo App APIs.
type DiscoveryService struct {
	profiles *profile.Manager
	devices  *device.Manager
	upstream *upstream.HongguoClient
	pageTTL  *cache.TTLCache[gateway.CatalogData]
	feedTTL  *cache.TTLCache[gateway.FeedData]

	mu           sync.Mutex
	boardsCache  *gateway.RankingBoardsData
	boardsCached time.Time
	pagedStates  map[string]*discoveryPageState
}

func NewDiscoveryService(profiles *profile.Manager, devices *device.Manager, up *upstream.HongguoClient) *DiscoveryService {
	return &DiscoveryService{
		profiles:    profiles,
		devices:     devices,
		upstream:    up,
		pageTTL:     cache.NewTTLCache[gateway.CatalogData](256),
		feedTTL:     cache.NewTTLCache[gateway.FeedData](128),
		pagedStates: make(map[string]*discoveryPageState),
	}
}

func (s *DiscoveryService) GetRankingBoards(ctx context.Context) (gateway.RankingBoardsData, error) {
	s.mu.Lock()
	if s.boardsCache != nil && time.Since(s.boardsCached) < 15*time.Minute {
		cached := *s.boardsCache
		s.mu.Unlock()
		return cached, nil
	}
	s.mu.Unlock()

	deadline := serviceDeadline(ctx, 15*time.Second)
	dev, err := s.devices.Get(ctx, deadline)
	if err != nil {
		return gateway.RankingBoardsData{}, MapError(err)
	}

	raw, err := s.upstream.FetchDiscoveryRaw(ctx, s.profiles.Current(), dev, "/reading/bookapi/plan/v", rankPlanParams("all", "ranklist_hot_sc"), deadline)
	if err != nil {
		return gateway.RankingBoardsData{}, MapError(err)
	}

	var parsed struct {
		Data []*discoveryCell `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return gateway.RankingBoardsData{}, &upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "ranking boards parse failed", Err: err}
	}

	cell := findContainerCell(parsed.Data)
	if cell == nil || cell.CellSelector == nil || cell.CellSelector.OuterRow == nil {
		return gateway.RankingBoardsData{}, &upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "ranking boards missing cell selector"}
	}

	var items []gateway.RankingBoard
	for _, group := range cell.CellSelector.OuterRow.Items {
		if _, ok := allowedRankingGroups[group.SelectorItemID]; !ok {
			continue
		}
		if group.SubCellSelector == nil || group.SubCellSelector.OuterRow == nil {
			continue
		}
		for _, board := range group.SubCellSelector.OuterRow.Items {
			if !rankingBoardItemPattern.MatchString(board.SelectorItemID) {
				continue
			}
			name := strings.TrimSpace(board.ShowName)
			if group.SelectorItemID != "all" && strings.TrimSpace(group.ShowName) != "" {
				name = strings.TrimSpace(group.ShowName) + "·" + name
			}
			items = append(items, gateway.RankingBoard{
				ID:          "hongguo-app:" + group.SelectorItemID + ":" + board.SelectorItemID,
				Name:        name,
				Description: strings.TrimSpace(board.SubTitle),
				Source:      gateway.SourceHongguo,
			})
		}
	}

	if len(items) == 0 {
		return gateway.RankingBoardsData{}, &upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "ranking boards empty"}
	}

	out := gateway.RankingBoardsData{Items: items}
	s.mu.Lock()
	s.boardsCache = &out
	s.boardsCached = time.Now()
	s.mu.Unlock()
	return out, nil
}

func (s *DiscoveryService) GetRankings(ctx context.Context, board string, page, limit int) (gateway.CatalogData, error) {
	board = strings.TrimSpace(board)
	if board == "" {
		board = "hongguo-app:all:ranklist_hot_sc"
	}
	matches := rankingBoardIDPattern.FindStringSubmatch(board)
	if len(matches) != 3 {
		return gateway.CatalogData{}, NewBadRequest("board 参数非法，请从榜单列表获取")
	}
	if page < 1 {
		return gateway.CatalogData{}, NewBadRequest("page 必须大于等于 1")
	}
	limit = normalizeLimit(limit, 40, 100)

	cacheKey := fmt.Sprintf("rank:%s:%d:%d", board, page, limit)
	if cached, ok := s.pageTTL.Get(cacheKey); ok {
		return cached, nil
	}

	deadline := serviceDeadline(ctx, 15*time.Second)
	dev, err := s.devices.Get(ctx, deadline)
	if err != nil {
		return gateway.CatalogData{}, MapError(err)
	}

	group, subBoard := matches[1], matches[2]
	res, err := s.loadPaged(ctx, "rank:"+board, page, func() (catalogPageResult, discoveryCursor, error) {
		params := rankPlanParams(group, subBoard)
		raw, err := s.upstream.FetchDiscoveryRaw(ctx, s.profiles.Current(), dev, "/reading/bookapi/plan/v", params, deadline)
		if err != nil {
			return catalogPageResult{}, discoveryCursor{}, err
		}
		var parsed struct {
			Data []*discoveryCell `json:"data"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return catalogPageResult{}, discoveryCursor{}, &upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "rankings parse failed", Err: err}
		}
		cell := findContainerCell(parsed.Data)
		if cell == nil {
			return catalogPageResult{}, discoveryCursor{}, &upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "rankings missing cell"}
		}
		if !validateRankingSelection(cell, group, subBoard) {
			return catalogPageResult{}, discoveryCursor{}, NewBadRequest("官方榜单配置已更新，请刷新榜单列表")
		}
		list := extractDiscoveryVideos(cell)
		nextOff := int(cell.NextOffset)
		hasMore := (cell.HasMore == nil || *cell.HasMore) && nextOff > 0 && len(list) > 0
		changeParams := append(append([]signer.QueryKV{}, params...), signer.QueryKV{Key: "tab_type", Value: "26"})
		return catalogPageResult{list: list, hasMore: hasMore}, discoveryCursor{
			Params:      changeParams,
			CellID:      cell.CellIDStr,
			Offset:      nextOff,
			SessionID:   cell.SessionID,
			RankVersion: cell.RankVersion,
		}, nil
	}, func(cur discoveryCursor) (catalogPageResult, discoveryCursor, error) {
		return s.callDiscoveryCellChange(ctx, dev, cur, deadline)
	})
	if err != nil {
		return gateway.CatalogData{}, MapError(err)
	}

	out := buildCatalogData(page, limit, res)
	s.pageTTL.Set(cacheKey, out, 5*time.Minute)
	return out, nil
}

func (s *DiscoveryService) GetRecommendations(ctx context.Context, rawGenre string, page, limit int) (gateway.CatalogData, error) {
	if page < 1 {
		return gateway.CatalogData{}, NewBadRequest("page 必须大于等于 1")
	}
	limit = normalizeLimit(limit, 40, 100)

	genre := strings.TrimSpace(rawGenre)
	switch genre {
	case "", "real-drama", "playlet":
		return s.getTabRecommendations(ctx, 38, 1, page, limit)
	case "comic-drama", "comic", "motion_comic", "comic_series":
		return s.getTabRecommendations(ctx, 32, 2, page, limit)
	case "ai-drama", "ai_playlet", "ai_series":
		return s.GetRankings(ctx, "hongguo-app:ai_playlet:ai_playlet_hot_sc", page, limit)
	case "all", "short_play":
		return s.GetRankings(ctx, "hongguo-app:all:ranklist_hot_sc", page, limit)
	default:
		return gateway.CatalogData{}, NewBadRequest("genre 仅支持 real-drama / comic-drama / ai-drama / all")
	}
}

func (s *DiscoveryService) getTabRecommendations(ctx context.Context, tabType, tabIndex, page, limit int) (gateway.CatalogData, error) {
	cacheKey := fmt.Sprintf("rec:%d:%d:%d", tabType, page, limit)
	if cached, ok := s.pageTTL.Get(cacheKey); ok {
		return cached, nil
	}

	deadline := serviceDeadline(ctx, 15*time.Second)
	dev, err := s.devices.Get(ctx, deadline)
	if err != nil {
		return gateway.CatalogData{}, MapError(err)
	}

	stateKey := fmt.Sprintf("rec:%d", tabType)
	res, err := s.loadPaged(ctx, stateKey, page, func() (catalogPageResult, discoveryCursor, error) {
		raw, err := s.upstream.FetchDiscoveryRaw(ctx, s.profiles.Current(), dev, "/reading/bookapi/bookmall/tab/v", []signer.QueryKV{
			{Key: "tab_type", Value: strconv.Itoa(tabType)},
			{Key: "tab_index", Value: strconv.Itoa(tabIndex)},
			{Key: "offset", Value: "0"},
		}, deadline)
		if err != nil {
			return catalogPageResult{}, discoveryCursor{}, err
		}
		var parsed struct {
			Data struct {
				TabItem []struct {
					TabType  resolver.FlexInt `json:"tab_type"`
					CellData []*discoveryCell `json:"cell_data"`
				} `json:"tab_item"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return catalogPageResult{}, discoveryCursor{}, &upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "recommendations parse failed", Err: err}
		}
		var cell *discoveryCell
		for _, tab := range parsed.Data.TabItem {
			if int(tab.TabType) == tabType {
				cell = findContainerCell(tab.CellData)
				break
			}
		}
		if cell == nil {
			return catalogPageResult{}, discoveryCursor{}, &upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "recommendations missing cell"}
		}
		list := extractDiscoveryVideos(cell)
		nextOff := int(cell.NextOffset)
		hasMore := (cell.HasMore == nil || *cell.HasMore) && nextOff > 0 && len(list) > 0
		return catalogPageResult{list: list, hasMore: hasMore}, discoveryCursor{
			Params: []signer.QueryKV{
				{Key: "tab_type", Value: strconv.Itoa(tabType)},
			},
			CellID:      cell.CellIDStr,
			Offset:      nextOff,
			SessionID:   cell.SessionID,
			RankVersion: cell.RankVersion,
		}, nil
	}, func(cur discoveryCursor) (catalogPageResult, discoveryCursor, error) {
		return s.callDiscoveryCellChange(ctx, dev, cur, deadline)
	})
	if err != nil {
		return gateway.CatalogData{}, MapError(err)
	}

	out := buildCatalogData(page, limit, res)
	s.pageTTL.Set(cacheKey, out, 5*time.Minute)
	return out, nil
}

func (s *DiscoveryService) GetFeed(ctx context.Context, rawScope string, page, limit int) (gateway.FeedData, error) {
	if page < 1 {
		return gateway.FeedData{}, NewBadRequest("page 必须大于等于 1")
	}
	limit = normalizeLimit(limit, 40, 100)

	scope := strings.TrimSpace(rawScope)
	if scope == "" {
		if page > 1 {
			return gateway.FeedData{}, NewBadRequest("翻页时请回传 scope 参数")
		}
		scope = fmt.Sprintf("feed_%d", time.Now().UnixNano())
	}

	cacheKey := fmt.Sprintf("feed:%s:%d:%d", scope, page, limit)
	if cached, ok := s.feedTTL.Get(cacheKey); ok {
		return cached, nil
	}

	deadline := serviceDeadline(ctx, 15*time.Second)
	dev, err := s.devices.Get(ctx, deadline)
	if err != nil {
		return gateway.FeedData{}, MapError(err)
	}

	res, err := s.loadPaged(ctx, "feed:"+scope, page, func() (catalogPageResult, discoveryCursor, error) {
		raw, err := s.upstream.FetchDiscoveryRaw(ctx, s.profiles.Current(), dev, "/reading/bookapi/bookmall/tab/v", []signer.QueryKV{
			{Key: "tab_type", Value: "16"},
			{Key: "bottom_tab_type", Value: "7"},
			{Key: "tab_index", Value: "0"},
			{Key: "offset", Value: "0"},
			{Key: "client_template", Value: "7"},
		}, deadline)
		if err != nil {
			return catalogPageResult{}, discoveryCursor{}, err
		}
		var parsed struct {
			Data struct {
				TabItem []struct {
					TabType         resolver.FlexInt `json:"tab_type"`
					NextOffset      resolver.FlexInt `json:"next_offset"`
					BottomUnlimited *bool            `json:"bottom_unlimited"`
					SessionID       string           `json:"session_id"`
					CellData        []*discoveryCell `json:"cell_data"`
				} `json:"tab_item"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return catalogPageResult{}, discoveryCursor{}, &upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "feed parse failed", Err: err}
		}

		var targetCell *discoveryCell
		var tabNextOff int
		var tabUnlimited bool
		var tabSessionID string
		for _, tab := range parsed.Data.TabItem {
			if int(tab.TabType) == 16 {
				tabNextOff = int(tab.NextOffset)
				tabUnlimited = tab.BottomUnlimited == nil || *tab.BottomUnlimited
				tabSessionID = tab.SessionID
				for _, c := range tab.CellData {
					if c != nil && c.CellIDStr != "" && len(extractDiscoveryVideos(c)) > 0 {
						targetCell = c
						break
					}
				}
				break
			}
		}
		if targetCell == nil {
			return catalogPageResult{}, discoveryCursor{}, &upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "feed missing cell"}
		}

		list := extractDiscoveryVideos(targetCell)
		nextOff := tabNextOff
		if len(list) > nextOff {
			nextOff = len(list)
		}
		if int(targetCell.NextOffset) > nextOff {
			nextOff = int(targetCell.NextOffset)
		}
		hasMore := tabUnlimited && len(list) > 0 && nextOff > 0

		return catalogPageResult{list: list, hasMore: hasMore}, discoveryCursor{
			Params: []signer.QueryKV{
				{Key: "tab_type", Value: "16"},
				{Key: "bottom_tab_type", Value: "7"},
				{Key: "client_template", Value: "7"},
				{Key: "client_req_type", Value: "2"},
			},
			CellID:    targetCell.CellIDStr,
			Offset:    nextOff,
			SessionID: firstNonEmpty(targetCell.SessionID, tabSessionID),
		}, nil
	}, func(cur discoveryCursor) (catalogPageResult, discoveryCursor, error) {
		return s.callDiscoveryCellChange(ctx, dev, cur, deadline)
	})
	if err != nil {
		return gateway.FeedData{}, MapError(err)
	}

	list := res.list
	if len(list) > limit {
		list = list[:limit]
	}
	pageCount := page
	if res.hasMore && len(list) > 0 {
		pageCount = 99
	}
	out := gateway.FeedData{
		Page:      page,
		PageCount: pageCount,
		HasMore:   res.hasMore && len(list) > 0,
		Scope:     scope,
		Total:     len(list),
		List:      list,
	}
	s.feedTTL.Set(cacheKey, out, 2*time.Minute)
	return out, nil
}

func (s *DiscoveryService) loadPaged(
	ctx context.Context,
	stateKey string,
	page int,
	fetchFirst func() (catalogPageResult, discoveryCursor, error),
	fetchNext func(cur discoveryCursor) (catalogPageResult, discoveryCursor, error),
) (catalogPageResult, error) {
	s.mu.Lock()
	st := s.pagedStates[stateKey]
	if page == 1 {
		st = &discoveryPageState{
			cursors: make(map[int]discoveryCursor),
			pages:   make(map[int]catalogPageResult),
			updated: time.Now(),
		}
		s.pagedStates[stateKey] = st
	} else {
		if st == nil || time.Since(st.updated) > 2*time.Hour {
			s.mu.Unlock()
			return catalogPageResult{}, NewBadRequest("翻页游标已过期，请从第 1 页重新翻页")
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

	var cur discoveryCursor
	if page > 1 {
		var ok bool
		cur, ok = st.cursors[page]
		if !ok {
			s.mu.Unlock()
			return catalogPageResult{}, NewBadRequest("翻页游标不存在，请从第 1 页顺序翻页")
		}
	}
	s.mu.Unlock()

	var res catalogPageResult
	var nextCur discoveryCursor
	var err error
	if page == 1 {
		res, nextCur, err = fetchFirst()
	} else {
		res, nextCur, err = fetchNext(cur)
	}
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

func (s *DiscoveryService) callDiscoveryCellChange(ctx context.Context, dev signer.Device, cur discoveryCursor, deadline time.Time) (catalogPageResult, discoveryCursor, error) {
	params := make([]signer.QueryKV, 0, len(cur.Params)+6)
	params = append(params, cur.Params...)
	params = append(params,
		signer.QueryKV{Key: "cell_id", Value: cur.CellID},
		signer.QueryKV{Key: "offset", Value: strconv.Itoa(cur.Offset)},
		signer.QueryKV{Key: "limit", Value: "40"},
		signer.QueryKV{Key: "change_type", Value: "1"},
	)
	if cur.SessionID != "" {
		params = append(params, signer.QueryKV{Key: "session_id", Value: cur.SessionID})
	}
	if cur.RankVersion != "" {
		params = append(params, signer.QueryKV{Key: "rank_version", Value: cur.RankVersion})
	}

	raw, err := s.upstream.FetchDiscoveryRaw(ctx, s.profiles.Current(), dev, "/reading/bookapi/bookmall/cell/change/v", params, deadline)
	if err != nil {
		return catalogPageResult{}, discoveryCursor{}, err
	}

	var parsed struct {
		Data struct {
			CellView    *discoveryCell   `json:"cell_view"`
			NextOffset  resolver.FlexInt `json:"next_offset"`
			HasMore     *bool            `json:"has_more"`
			SessionID   string           `json:"session_id"`
			RankVersion string           `json:"rank_version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Data.CellView == nil {
		return catalogPageResult{}, discoveryCursor{}, &upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "cell_change missing cell_view", Err: err}
	}

	cv := parsed.Data.CellView
	list := extractDiscoveryVideos(cv)
	nextOff := int(parsed.Data.NextOffset)
	if nextOff == 0 {
		nextOff = int(cv.NextOffset)
	}
	dataMore := parsed.Data.HasMore == nil || *parsed.Data.HasMore
	cellMore := cv.HasMore == nil || *cv.HasMore
	hasMore := dataMore && cellMore && nextOff > cur.Offset && len(list) > 0

	nextCur := discoveryCursor{
		Params:      cur.Params,
		CellID:      firstNonEmpty(cv.CellIDStr, cur.CellID),
		Offset:      nextOff,
		SessionID:   firstNonEmpty(parsed.Data.SessionID, cv.SessionID, cur.SessionID),
		RankVersion: firstNonEmpty(parsed.Data.RankVersion, cv.RankVersion, cur.RankVersion),
	}
	return catalogPageResult{list: list, hasMore: hasMore}, nextCur, nil
}

func rankPlanParams(selected, sub string) []signer.QueryKV {
	return []signer.QueryKV{
		{Key: "scene", Value: "10"},
		{Key: "from", Value: "video_ranklist"},
		{Key: "bookstore_tab_type", Value: "26"},
		{Key: "selected_items", Value: selected},
		{Key: "sub_selected_items", Value: sub},
		{Key: "panel_selected_items", Value: ""},
		{Key: "background_selected_items", Value: ""},
	}
}

func findContainerCell(cells []*discoveryCell) *discoveryCell {
	for _, c := range cells {
		if c != nil && c.CellIDStr != "" && (c.CellData != nil || len(c.VideoData) > 0 || c.CellSelector != nil) {
			return c
		}
	}
	return nil
}

func validateRankingSelection(cell *discoveryCell, group, board string) bool {
	if cell == nil || cell.CellSelector == nil || cell.CellSelector.OuterRow == nil {
		return false
	}
	for _, g := range cell.CellSelector.OuterRow.Items {
		if g.IsSelected {
			if g.SelectorItemID != group {
				return false
			}
			if g.SubCellSelector == nil || g.SubCellSelector.OuterRow == nil {
				return false
			}
			for _, b := range g.SubCellSelector.OuterRow.Items {
				if b.IsSelected {
					return b.SelectorItemID == board
				}
			}
			return false
		}
	}
	return false
}

func extractDiscoveryVideos(root *discoveryCell) []gateway.SeriesSummary {
	var out []gateway.SeriesSummary
	seen := make(map[string]struct{})
	var visit func(c *discoveryCell)
	visit = func(c *discoveryCell) {
		if c == nil {
			return
		}
		for _, v := range c.VideoData {
			var dSeriesIDStr, dSeriesID, dTitle, dCover, dIntro string
			var dEpCnt int
			if v.VideoDetail != nil {
				dSeriesIDStr = v.VideoDetail.SeriesIDStr
				dSeriesID = v.VideoDetail.SeriesID
				dTitle = v.VideoDetail.SeriesTitle
				dCover = v.VideoDetail.SeriesCover
				dIntro = v.VideoDetail.SeriesIntro
				dEpCnt = int(v.VideoDetail.EpisodeCnt)
			}
			id := strings.TrimSpace(firstNonEmpty(dSeriesIDStr, v.SeriesIDStr, dSeriesID, v.SeriesID))
			title := cleanText(firstNonEmpty(dTitle, v.Title))
			if !isValidID(id) || title == "" || v.DisablePlay {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}

			epCnt := int(v.EpisodeCnt)
			if dEpCnt > 0 {
				epCnt = dEpCnt
			}
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
				Cover:        cleanCoverURL(firstNonEmpty(dCover, v.Cover)),
				EpisodeCount: epCnt,
				Remark:       remark,
				Intro:        cleanText(firstNonEmpty(dIntro, v.VideoDesc)),
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

func buildCatalogData(page, limit int, res catalogPageResult) gateway.CatalogData {
	list := res.list
	if len(list) > limit {
		list = list[:limit]
	}
	pageCount := page
	if res.hasMore && len(list) > 0 {
		pageCount = 99
	}
	return gateway.CatalogData{
		Page:      page,
		PageCount: pageCount,
		Total:     len(list),
		List:      list,
	}
}

func normalizeLimit(limit, defVal, maxVal int) int {
	if limit <= 0 {
		return defVal
	}
	if limit > maxVal {
		return maxVal
	}
	return limit
}

func serviceDeadline(ctx context.Context, d time.Duration) time.Time {
	deadline := time.Now().Add(d)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		return dl
	}
	return deadline
}
