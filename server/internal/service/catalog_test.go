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
	"time"

	"mioreel-gateway/server/internal/config"
	"mioreel-gateway/server/internal/device"
	"mioreel-gateway/server/internal/observability"
	"mioreel-gateway/server/internal/profile"
	"mioreel-gateway/server/internal/upstream"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCatalogCursorRulesAndSeriesDisablePlay(t *testing.T) {
	rt := rtFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/service/2/device_register/"):
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"install_id_str":"4069772115858074","device_id_str":"4069772115853978"}`)),
			}, nil
		case strings.Contains(r.URL.Path, "/reading/bookapi/bookmall/tab/v"):
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"tab_item":[{"tab_type":8,"cell_data":[{"cell_id_str":"6952850996422770718","cell_data":[]}]}]}}`)),
			}, nil
		case strings.Contains(r.URL.Path, "/reading/bookapi/bookmall/cell/change/v"):
			return &http.Response{
				StatusCode: 200,
				Body: io.NopCloser(strings.NewReader(`{
					"code":0,
					"data":{
						"next_offset":40,
						"has_more":true,
						"cell_view":{
							"cell_id_str":"6952850996422770718",
							"has_more":true,
							"video_data":[
								{"series_id_str":"7701678491724418073","title":"测试短剧","episode_cnt":10}
							]
						}
					}
				}`)),
			}, nil
		case strings.Contains(r.URL.Path, "/novel/player/video_detail/v1/"):
			return &http.Response{
				StatusCode: 200,
				Body: io.NopCloser(strings.NewReader(`{
					"code":0,
					"data":{"video_data":{
						"series_id":"7701678491724418073",
						"series_title":"测试短剧",
						"episode_cnt":2,
						"video_list":[
							{"vid":"7701680675979545625","title":"第1集","disable_play":false},
							{"vid":"7701680675979545626","title":"第2集","disable_play":true}
						]
					}}
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

	catSvc := NewCatalogService(profMgr, devMgr, hg)
	// Jumping directly to page 2 without page 1 must return 40001
	_, err := catSvc.GetCatalog(context.Background(), "real-drama", 2, 40)
	var ge *GatewayError
	if !errors.As(err, &ge) || ge.Code != 40001 {
		t.Fatalf("expected 40001 on page jump, got %v", err)
	}

	// Sequential page 1 -> page 2 succeeds
	p1, err := catSvc.GetCatalog(context.Background(), "real-drama", 1, 40)
	if err != nil || len(p1.List) != 1 || p1.List[0].Tags == nil {
		t.Fatalf("page 1 failed: err=%v data=%+v", err, p1)
	}
	// Expire cursor manually and verify page 2 returns 40001
	catSvc.mu.Lock()
	catSvc.genreState["playlet"].updated = time.Now().Add(-3 * time.Hour)
	catSvc.mu.Unlock()
	_, err = catSvc.GetCatalog(context.Background(), "real-drama", 2, 40)
	if !errors.As(err, &ge) || ge.Code != 40001 {
		t.Fatalf("expected 40001 on expired cursor, got %v", err)
	}

	// Series disable_play maps to Available=false
	serSvc := NewSeriesService(profMgr, devMgr, hg)
	ser, err := serSvc.GetSeries(context.Background(), "hongguo", "7701678491724418073")
	if err != nil {
		t.Fatalf("GetSeries failed: %v", err)
	}
	if len(ser.Episodes) != 2 || !ser.Episodes[0].Available || ser.Episodes[1].Available {
		t.Fatalf("unexpected episodes availability: %+v", ser.Episodes)
	}
}
