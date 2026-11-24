package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"mioreel-gateway/server/internal/config"
	"mioreel-gateway/server/internal/device"
	"mioreel-gateway/server/internal/observability"
	"mioreel-gateway/server/internal/profile"
	"mioreel-gateway/server/internal/upstream"
)

func TestDiscoveryRankingsRecommendationsAndFeed(t *testing.T) {
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		switch {
		case strings.Contains(r.URL.Path, "/service/2/device_register/"):
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"install_id_str":"4069772115858074","device_id_str":"4069772115853978"}`)),
			}, nil
		case strings.Contains(r.URL.Path, "/reading/bookapi/plan/v"):
			sel := q.Get("selected_items")
			sub := q.Get("sub_selected_items")
			if sel == "" {
				sel = "all"
			}
			if sub == "" {
				sub = "ranklist_hot_sc"
			}
			body := `{
				"code": 0,
				"data": [{
					"cell_id_str": "6952850996422770999",
					"next_offset": 20,
					"has_more": true,
					"session_id": "sess_rank_1",
					"cell_selector": {
						"outer_row": {
							"items": [
								{
									"selector_item_id": "all",
									"show_name": "全部",
									"is_selected": ` + boolStr(sel == "all") + `,
									"sub_cell_selector": {
										"outer_row": {
											"items": [
												{"selector_item_id": "ranklist_hot_sc", "show_name": "热播榜", "sub_title": "每日更新", "is_selected": ` + boolStr(sel == "all" && sub == "ranklist_hot_sc") + `}
											]
										}
									}
								},
								{
									"selector_item_id": "ai_playlet",
									"show_name": "AI剧",
									"is_selected": ` + boolStr(sel == "ai_playlet") + `,
									"sub_cell_selector": {
										"outer_row": {
											"items": [
												{"selector_item_id": "ai_playlet_hot_sc", "show_name": "AI热播榜", "sub_title": "每日更新", "is_selected": ` + boolStr(sel == "ai_playlet" && sub == "ai_playlet_hot_sc") + `}
											]
										}
									}
								},
								{
									"selector_item_id": "ignored_group",
									"show_name": "忽略",
									"sub_cell_selector": {"outer_row": {"items": [{"selector_item_id": "x", "show_name": "X"}]}}
								}
							]
						}
					},
					"video_data": [
						{"series_id_str": "7701678491724418073", "title": "榜单热剧1", "episode_cnt": 60, "sub_title": "都市"}
					]
				}]
			}`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil

		case strings.Contains(r.URL.Path, "/reading/bookapi/bookmall/tab/v"):
			tabType := q.Get("tab_type")
			switch tabType {
			case "38":
				return &http.Response{
					StatusCode: 200,
					Body: io.NopCloser(strings.NewReader(`{
						"code": 0,
						"data": {"tab_item": [{"tab_type": 38, "cell_data": [{
							"cell_id_str": "cell_rec_38",
							"next_offset": 20,
							"has_more": true,
							"video_data": [{"series_id_str": "7701678491724418080", "title": "精选真人短剧", "episode_cnt": 50}]
						}]}]}
					}`)),
				}, nil
			case "32":
				return &http.Response{
					StatusCode: 200,
					Body: io.NopCloser(strings.NewReader(`{
						"code": 0,
						"data": {"tab_item": [{"tab_type": 32, "cell_data": [{
							"cell_id_str": "cell_rec_32",
							"next_offset": 20,
							"has_more": false,
							"video_data": [{"series_id_str": "7701678491724418081", "title": "精选漫剧", "episode_cnt": 30}]
						}]}]}
					}`)),
				}, nil
			case "16":
				return &http.Response{
					StatusCode: 200,
					Body: io.NopCloser(strings.NewReader(`{
						"code": 0,
						"data": {"tab_item": [{
							"tab_type": 16,
							"next_offset": 10,
							"bottom_unlimited": true,
							"session_id": "sess_feed_1",
							"cell_data": [{
								"cell_id_str": "cell_feed_16",
								"video_data": [{
									"video_detail": {
										"series_id_str": "7701678491724418082",
										"series_title": "首页推荐流短剧",
										"episode_cnt": 88,
										"series_intro": "好看"
									}
								}]
							}]
						}]}
					}`)),
				}, nil
			}
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil

		case strings.Contains(r.URL.Path, "/reading/bookapi/bookmall/cell/change/v"):
			return &http.Response{
				StatusCode: 200,
				Body: io.NopCloser(strings.NewReader(`{
					"code": 0,
					"data": {
						"next_offset": 40,
						"has_more": false,
						"cell_view": {
							"cell_id_str": "` + q.Get("cell_id") + `",
							"has_more": false,
							"video_data": [
								{"series_id_str": "7701678491724418099", "title": "第二页短剧", "episode_cnt": 25}
							]
						}
					}
				}`)),
			}, nil

		default:
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
	})

	logger := slog.New(slog.DiscardHandler)
	metrics := observability.NewMetrics()
	cfg := config.Default()
	profMgr := profile.NewManager(cfg, logger, metrics)
	cli := upstream.NewClientWithTransport(rt)
	hg := upstream.NewHongguoClient(cli, metrics)
	devMgr := device.NewManager(cli, filepath.Join(t.TempDir(), "dev.json"), logger)

	svc := NewDiscoveryService(profMgr, devMgr, hg)
	ctx := context.Background()

	// 1. RankingBoards
	boards, err := svc.GetRankingBoards(ctx)
	if err != nil {
		t.Fatalf("GetRankingBoards failed: %v", err)
	}
	if len(boards.Items) != 2 {
		t.Fatalf("expected 2 ranking boards, got %d: %+v", len(boards.Items), boards.Items)
	}
	if boards.Items[0].ID != "hongguo-app:all:ranklist_hot_sc" || boards.Items[1].Name != "AI剧·AI热播榜" {
		t.Fatalf("unexpected boards: %+v", boards.Items)
	}

	// 2. Rankings (page 1 -> page 2, invalid board -> 40001)
	var ge *GatewayError
	if _, err := svc.GetRankings(ctx, "invalid-board", 1, 40); !errors.As(err, &ge) || ge.Code != 40001 {
		t.Fatalf("expected 40001 for invalid board, got %v", err)
	}
	r1, err := svc.GetRankings(ctx, "hongguo-app:all:ranklist_hot_sc", 1, 40)
	if err != nil || len(r1.List) != 1 || r1.List[0].Title != "榜单热剧1" {
		t.Fatalf("Rankings page 1 failed: err=%v data=%+v", err, r1)
	}
	r2, err := svc.GetRankings(ctx, "hongguo-app:all:ranklist_hot_sc", 2, 40)
	if err != nil || len(r2.List) != 1 || r2.List[0].Title != "第二页短剧" {
		t.Fatalf("Rankings page 2 failed: err=%v data=%+v", err, r2)
	}

	// 3. Recommendations
	recReal, err := svc.GetRecommendations(ctx, "real-drama", 1, 40)
	if err != nil || len(recReal.List) != 1 || recReal.List[0].Title != "精选真人短剧" {
		t.Fatalf("Recommendations real-drama failed: err=%v data=%+v", err, recReal)
	}
	recComic, err := svc.GetRecommendations(ctx, "comic-drama", 1, 40)
	if err != nil || len(recComic.List) != 1 || recComic.List[0].Title != "精选漫剧" {
		t.Fatalf("Recommendations comic-drama failed: err=%v data=%+v", err, recComic)
	}
	recAI, err := svc.GetRecommendations(ctx, "ai-drama", 1, 40)
	if err != nil || len(recAI.List) != 1 {
		t.Fatalf("Recommendations ai-drama failed: err=%v data=%+v", err, recAI)
	}

	// 4. Feed
	f1, err := svc.GetFeed(ctx, "", 1, 40)
	if err != nil || len(f1.List) != 1 || !f1.HasMore || f1.Scope == "" || f1.List[0].Title != "首页推荐流短剧" {
		t.Fatalf("Feed page 1 failed: err=%v data=%+v", err, f1)
	}
	f2, err := svc.GetFeed(ctx, f1.Scope, 2, 40)
	if err != nil || len(f2.List) != 1 || f2.HasMore || f2.List[0].Title != "第二页短剧" {
		t.Fatalf("Feed page 2 failed: err=%v data=%+v", err, f2)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
