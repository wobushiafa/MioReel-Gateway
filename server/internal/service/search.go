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
	"mioreel-gateway/server/internal/device"
	"mioreel-gateway/server/internal/profile"
	"mioreel-gateway/server/internal/resolver"
	"mioreel-gateway/server/internal/signer"
	"mioreel-gateway/server/internal/upstream"
)

type searchCursor struct {
	Offset   int
	Passback string
	SearchID string
	General  bool
}

type searchState struct {
	pages   map[int]gateway.SearchData
	cursors map[int]searchCursor
	endPage int
	updated time.Time
}

// SearchService implements GET /api/v1/search?q=.
type SearchService struct {
	profiles *profile.Manager
	devices  *device.Manager
	upstream *upstream.HongguoClient

	mu     sync.Mutex
	states map[string]*searchState
}

func NewSearchService(profiles *profile.Manager, devices *device.Manager, up *upstream.HongguoClient) *SearchService {
	return &SearchService{
		profiles: profiles,
		devices:  devices,
		upstream: up,
		states:   make(map[string]*searchState),
	}
}

func (s *SearchService) Search(ctx context.Context, rawQuery string, page int) (gateway.SearchData, error) {
	query := strings.TrimSpace(rawQuery)
	if len(query) > 100 {
		query = string([]rune(query)[:100])
	}
	if page < 1 {
		page = 1
	}
	if query == "" {
		return gateway.SearchData{
			Query:   "",
			Page:    page,
			HasMore: false,
			Total:   0,
			List:    []gateway.SeriesSummary{},
		}, nil
	}

	s.mu.Lock()
	st := s.states[query]
	if page == 1 || st == nil || time.Since(st.updated) > 15*time.Minute {
		st = &searchState{
			pages:   make(map[int]gateway.SearchData),
			cursors: map[int]searchCursor{1: {}},
			updated: time.Now(),
		}
		s.states[query] = st
	}
	if st.endPage > 0 && page > st.endPage {
		s.mu.Unlock()
		return gateway.SearchData{Query: query, Page: page, HasMore: false, Total: 0, List: []gateway.SeriesSummary{}}, nil
	}
	if cached, ok := st.pages[page]; ok {
		s.mu.Unlock()
		return cached, nil
	}
	cur, hasCur := st.cursors[page]
	s.mu.Unlock()

	if !hasCur {
		return gateway.SearchData{}, NewBadRequest("搜索游标已过期，请从第 1 页重新搜索")
	}

	deadline := time.Now().Add(20 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	dev, err := s.devices.Get(ctx, deadline)
	if err != nil {
		return gateway.SearchData{}, MapError(err)
	}

	res, nextCur, hasMore, err := s.fetchPage(ctx, dev, query, cur, deadline)
	if err != nil {
		return gateway.SearchData{}, MapError(err)
	}

	// Fallback to general tab (tab_type=11) if page 1 feed search returned empty
	if page == 1 && !cur.General && len(res) == 0 {
		if fbRes, fbNext, fbMore, fbErr := s.fetchPage(ctx, dev, query, searchCursor{General: true}, deadline); fbErr == nil && len(fbRes) > 0 {
			res = fbRes
			nextCur = fbNext
			hasMore = fbMore
		}
	}

	out := gateway.SearchData{
		Query:   query,
		Page:    page,
		HasMore: hasMore,
		Total:   len(res),
		List:    res,
	}

	s.mu.Lock()
	st.pages[page] = out
	if hasMore {
		st.cursors[page+1] = nextCur
	} else {
		st.endPage = page
	}
	st.updated = time.Now()
	s.mu.Unlock()

	return out, nil
}

type rawSearchTab struct {
	TabType    resolver.FlexInt `json:"tab_type"`
	Title      string           `json:"title"`
	NextOffset resolver.FlexInt `json:"next_offset"`
	HasMore    bool             `json:"has_more"`
	Passback   string           `json:"passback"`
	SearchID   string           `json:"search_id"`
	Data       []struct {
		VideoData []struct {
			SeriesID    string `json:"series_id"`
			Title       string `json:"title"`
			Cover       string `json:"cover"`
			EpisodeCnt  int    `json:"episode_cnt"`
			VideoDetail *struct {
				SeriesID    string `json:"series_id"`
				SeriesIDStr string `json:"series_id_str"`
				SeriesTitle string `json:"series_title"`
				SeriesCover string `json:"series_cover"`
				SeriesIntro string `json:"series_intro"`
				EpisodeCnt  int    `json:"episode_cnt"`
			} `json:"video_detail"`
		} `json:"video_data"`
	} `json:"data"`
}

func (s *SearchService) fetchPage(ctx context.Context, dev signer.Device, query string, cur searchCursor, deadline time.Time) ([]gateway.SeriesSummary, searchCursor, bool, error) {
	tabType := "29"
	tabName := "feed"
	if cur.General {
		tabType = "11"
		tabName = ""
	}
	firstEnter := "false"
	if cur.Offset == 0 {
		firstEnter = "true"
	}
	params := []signer.QueryKV{
		{Key: "query", Value: query},
		{Key: "count", Value: "0"},
		{Key: "offset", Value: strconv.Itoa(cur.Offset)},
		{Key: "tab_type", Value: tabType},
		{Key: "tab_name", Value: tabName},
		{Key: "search_source", Value: "1"},
		{Key: "use_correct", Value: "false"},
		{Key: "use_lynx", Value: "false"},
		{Key: "user_is_login", Value: "0"},
		{Key: "bookshelf_search_plan", Value: "4"},
		{Key: "bookstore_tab", Value: "16"},
		{Key: "is_first_enter_search", Value: firstEnter},
	}
	if cur.Passback != "" {
		params = append(params, signer.QueryKV{Key: "passback", Value: cur.Passback})
	}
	if cur.SearchID != "" {
		params = append(params, signer.QueryKV{Key: "search_id", Value: cur.SearchID})
	}

	raw, err := s.upstream.FetchSearchRaw(ctx, s.profiles.Current(), dev, params, deadline)
	if err != nil {
		return nil, searchCursor{}, false, err
	}

	var parsed struct {
		SearchTabs []rawSearchTab `json:"search_tabs"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, searchCursor{}, false, &upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "search parse failed", Err: err}
	}

	var chosen *rawSearchTab
	wantType := 29
	if cur.General {
		wantType = 11
	}
	for i := range parsed.SearchTabs {
		if int(parsed.SearchTabs[i].TabType) == wantType {
			chosen = &parsed.SearchTabs[i]
			break
		}
	}
	if chosen == nil && len(parsed.SearchTabs) > 0 {
		chosen = &parsed.SearchTabs[0]
	}
	if chosen == nil {
		return []gateway.SeriesSummary{}, searchCursor{}, false, nil
	}

	list := extractSearchItems(*chosen)
	nextOff := int(chosen.NextOffset)
	hasMore := chosen.HasMore && nextOff > cur.Offset
	nextCur := searchCursor{
		Offset:   nextOff,
		Passback: chosen.Passback,
		SearchID: chosen.SearchID,
		General:  cur.General,
	}
	return list, nextCur, hasMore, nil
}

func extractSearchItems(tab rawSearchTab) []gateway.SeriesSummary {
	var out []gateway.SeriesSummary
	seen := make(map[string]struct{})
	for _, block := range tab.Data {
		for _, vd := range block.VideoData {
			var id, title, cover, intro string
			var epCnt int
			if vd.VideoDetail != nil {
				id = firstNonEmpty(vd.VideoDetail.SeriesIDStr, vd.SeriesID, vd.VideoDetail.SeriesID)
				title = cleanText(firstNonEmpty(vd.VideoDetail.SeriesTitle, vd.Title))
				cover = cleanCoverURL(firstNonEmpty(vd.VideoDetail.SeriesCover, vd.Cover))
				intro = cleanText(vd.VideoDetail.SeriesIntro)
				epCnt = vd.VideoDetail.EpisodeCnt
			} else {
				id = vd.SeriesID
				title = cleanText(vd.Title)
				cover = cleanCoverURL(vd.Cover)
				epCnt = vd.EpisodeCnt
			}
			if epCnt <= 0 {
				epCnt = vd.EpisodeCnt
			}
			if !isValidID(id) || title == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			remark := ""
			if epCnt > 0 {
				remark = fmt.Sprintf("全%d集", epCnt)
			}
			out = append(out, gateway.SeriesSummary{
				SeriesID:     id,
				Title:        title,
				Cover:        cover,
				EpisodeCount: epCnt,
				Remark:       remark,
				Intro:        intro,
				Tags:         []string{},
			})
		}
	}
	if out == nil {
		out = []gateway.SeriesSummary{}
	}
	return out
}
