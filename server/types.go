// Package api 定义 mioreel-gateway 的对外 HTTP 契约。
//
// 本文件是 docs/02-client-api.md 与 docs/07-source-browsing.md 的可执行形式：
// 字段名、JSON tag 与文档一一对应，改动必须同步改文档。
//
// 校验约定：
//   - 所有业务失败仍返回 HTTP 200，由 APIResponse.Code 表达；
//     /health、/media、/metrics 除外。
//   - 时间戳一律为 Unix 毫秒（int64）。
package api

// Source 标识一个视频源。首版仅支持 SourceHongguo。
//
// 使用类型别名（而非独立类型）以保证与内部 service 层的 string 互相赋值。
type Source = string

const (
	// SourceHongguo 红果短剧。
	SourceHongguo Source = "hongguo"
)

// APIResponse 是所有 /api/v1 端点的统一响应包络。
type APIResponse[T any] struct {
	// Code 为 0 表示成功；非 0 见 docs/05-error-codes.md。
	Code int `json:"code"`
	// Message 人类可读描述；成功时为 "ok"。
	Message string `json:"message"`
	// RequestID 排障追踪 ID。
	RequestID string `json:"request_id"`
	// Data 业务数据；失败时为零值。
	Data T `json:"data"`
}

// ---------------------------------------------------------------------------
// GET /api/v1/health
// ---------------------------------------------------------------------------

// HealthResponse 是健康检查响应。
type HealthResponse struct {
	Status   string         `json:"status"` // ok | degraded | down
	Version  string         `json:"version"`
	UptimeMs int64          `json:"uptime_ms"`
	Signing  SigningHealth  `json:"signing"`
	Upstream UpstreamHealth `json:"upstream"`
}

// SigningHealth 描述当前签名材料。
type SigningHealth struct {
	// ProfileID 为签名材料 id（十六进制字符串），内置值为 "646637794"。
	ProfileID string `json:"profile_id"`
	// SDKVersion 固定 "v04.10.03-ml-android"。
	SDKVersion string `json:"sdk_version"`
	// RotatedAt 为材料最近一次轮换时间（RFC3339）。
	RotatedAt string `json:"rotated_at"`
}

// UpstreamHealth 描述上游连通性。
type UpstreamHealth struct {
	// DeviceReady 表示设备三元组是否就绪。
	DeviceReady bool `json:"device_ready"`
	// LastSuccessAt 为最近一次上游成功时间（RFC3339），无则为空串。
	LastSuccessAt string `json:"last_success_at"`
	// ConsecutiveFailures 为连续失败次数。
	ConsecutiveFailures int64 `json:"consecutive_failures"`
}

// ---------------------------------------------------------------------------
// GET /api/v1/catalog
// ---------------------------------------------------------------------------

// CatalogRequest 是分类列表查询参数。
type CatalogRequest struct {
	// Category 取值 real-drama | comic-drama | ai-drama | all，默认 real-drama。
	Category string `form:"category"`
	// Page 从 1 起，默认 1。
	Page int `form:"page"`
	// Limit 默认 40，上限 100。
	Limit int `form:"limit"`
}

// CatalogData 是分类列表响应。
type CatalogData struct {
	Page int `json:"page"`
	// PageCount 为上游软上限，不代表真实总页数；应以 list 是否为空作为终止条件。
	PageCount int `json:"page_count"`
	// Total 为本次返回的条目数。
	Total int             `json:"total"`
	List  []SeriesSummary `json:"list"`
}

// SeriesSummary 是一个剧目摘要，catalog 与 search 共用。
type SeriesSummary struct {
	// SeriesID 为剧集号，10~30 位数字；解析直链的入口参数。
	SeriesID string `json:"series_id"`
	// Title 剧名。
	Title string `json:"title"`
	// Cover 封面图 URL。
	Cover string `json:"cover"`
	// EpisodeCount 总集数，0 表示未知。
	EpisodeCount int `json:"episode_count"`
	// Remark 如 "全80集"。
	Remark string `json:"remark"`
	// Intro 简介。
	Intro string `json:"intro"`
	// Tags 标签数组，永不为 null。
	Tags []string `json:"tags"`
	// Year 年份，恒为空字符串（上游未提供）。
	Year string `json:"year"`
}

// ---------------------------------------------------------------------------
// POST /api/v1/series
// ---------------------------------------------------------------------------

// SeriesRequest 是剧集详情请求。
type SeriesRequest struct {
	// Source 数据源标识，默认 hongguo。
	Source Source `json:"source,omitempty"`
	// SeriesID 为剧集号，10~30 位数字。
	SeriesID string `json:"series_id"`
}

// SeriesData 是剧集详情响应。
type SeriesData struct {
	SeriesID           string        `json:"series_id"`
	Title              string        `json:"title"`
	Cover              string        `json:"cover"`
	Intro              string        `json:"intro"`
	TotalEpisodes      int           `json:"total_episodes"`
	AccessibleEpisodes int           `json:"accessible_episodes"`
	Episodes           []EpisodeInfo `json:"episodes"`
}

// EpisodeInfo 是一集。
type EpisodeInfo struct {
	// No 为集序号，从 1 起。
	No int `json:"no"`
	// VID 是解析直链的真正凭据，客户端应持久化。
	VID string `json:"vid"`
	// Title 如 "第1集"。
	Title string `json:"title"`
	// Available 表示该集是否可播。
	Available bool `json:"available"`
}

// ---------------------------------------------------------------------------
// POST /api/v1/resolve   ★ 核心
// ---------------------------------------------------------------------------

// ResolveRequest 是解析播放直链的请求。
//
// 入参二选一：
//   - SeriesID + EpisodeNo
//   - VID（客户端已缓存分集表时）
type ResolveRequest struct {
	// Source 数据源标识，默认 hongguo。
	Source Source `json:"source,omitempty"`
	// SeriesID 剧集号。
	SeriesID string `json:"series_id,omitempty"`
	// EpisodeNo 集序号，从 1 起。
	EpisodeNo int `json:"episode_no,omitempty"`
	// VID 直接指定分集 id。
	VID string `json:"vid,omitempty"`
	// Quality 目标短边像素；0 表示取可用最高。
	Quality int `json:"quality,omitempty"`
	// PreferCodec 编码偏好顺序，默认 ["h264","hevc","mp4"]。
	PreferCodec []string `json:"prefer_codec,omitempty"`
	// TimeoutMs 客户端等待上限，默认取服务端配置。
	TimeoutMs int `json:"timeout_ms,omitempty"`
	// AvoidURL 上一次失败的直链；非空时本次跳过该 URL 且不写入缓存。
	AvoidURL string `json:"avoid_url,omitempty"`
}

// ResolveData 是解析结果。
type ResolveData struct {
	Source       Source `json:"source"`
	SeriesID     string `json:"series_id"`
	SeriesTitle  string `json:"series_title"`
	EpisodeNo    int    `json:"episode_no"`
	EpisodeID    string `json:"episode_id"`
	EpisodeTitle string `json:"episode_title"`

	// PlayURL 是 CDN 直链。客户端原样使用，不要改写。
	PlayURL string `json:"play_url"`
	// PlayHeaders 播放时需附加的 HTTP 头。
	// 注意：不包含 Referer —— 客户端不得自行添加。
	PlayHeaders map[string]string `json:"play_headers"`
	// DecryptionKey 是 AES-128 内容密钥（32 位 hex）。空串表示未加密。
	DecryptionKey string `json:"decryption_key"`
	// Encryption 取值 aes-128-cbc | none。
	Encryption string `json:"encryption"`

	// MediaType 形如 video/mp4。
	MediaType string `json:"media_type"`
	// SizeBytes 字节数，0 表示未知。
	SizeBytes int64 `json:"size_bytes"`

	// Quality 短边像素。
	Quality int `json:"quality"`
	// Width / Height 分辨率。
	Width  int `json:"width"`
	Height int `json:"height"`
	// Codec 形如 h264 | hevc | mp4。
	Codec string `json:"codec"`
	// DurationMs 时长，0 表示未知。
	DurationMs int64 `json:"duration_ms"`

	// ExpiresAt 直链失效时间（Unix 毫秒）。
	ExpiresAt int64 `json:"expires_at"`
	// ExpiresInMs 剩余有效期。
	ExpiresInMs int64 `json:"expires_in_ms"`

	// Variants 同集所有可用清晰度。首项即选中项。
	Variants []Variant `json:"variants"`

	// DanmakuID 弹幕关联 ID，形如 hongguo:{series_id}:{vid}。
	DanmakuID string `json:"danmaku_id"`

	// MediaTicket 浏览器预解密代理票据（启用媒体代理时下发，否则为空串）。
	MediaTicket string `json:"media_ticket,omitempty"`
}

// Variant 是一条可用播放流。
type Variant struct {
	URL           string `json:"url"`
	Quality       int    `json:"quality"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	Codec         string `json:"codec"`
	DecryptionKey string `json:"decryption_key"`
	ExpiresAt     int64  `json:"expires_at"`
}

// ---------------------------------------------------------------------------
// GET /api/v1/search
// ---------------------------------------------------------------------------

// SearchRequest 是搜索查询参数。
type SearchRequest struct {
	// Query 关键词。
	Query string `form:"q"`
	// Page 从 1 起。
	Page int `form:"page"`
	// Limit 默认 10。
	Limit int `form:"limit"`
}

// SearchData 是搜索结果。
type SearchData struct {
	Query string `json:"query"`
	Page  int    `json:"page"`
	// HasMore 表示是否还有下一页。
	HasMore bool `json:"has_more"`
	// Total 为本次返回的条目数。
	Total int             `json:"total"`
	List  []SeriesSummary `json:"list"`
}

// ---------------------------------------------------------------------------
// GET /api/v1/danmaku
// ---------------------------------------------------------------------------

// DanmakuData 是弹幕片段。
type DanmakuData struct {
	// EpisodeID 形如 hongguo:{series_id}:{vid}。
	EpisodeID string `json:"episode_id"`
	// StartMs 本片段时间起点（毫秒）。
	StartMs int64 `json:"start_ms"`
	// NextMs 下一片段时间起点（毫秒）。
	NextMs int64 `json:"next_ms"`
	// Items 弹幕条目，最多 90 条。
	Items []DanmakuItem `json:"items"`
}

// DanmakuItem 是一条弹幕。
type DanmakuItem struct {
	// ID 弹幕唯一 id。
	ID string `json:"id"`
	// TimeMs 相对剧集起点的毫秒偏移。
	TimeMs int64 `json:"time_ms"`
	// Text 弹幕文本。
	Text string `json:"text"`
	// Color 颜色，#RRGGBB。
	Color string `json:"color"`
}

// ---------------------------------------------------------------------------
// GET /api/v1/media/{ticket}   (可选，仅 Web 客户端)
// ---------------------------------------------------------------------------

// MediaTicket 是服务端签发的一次性媒体票据。
type MediaTicket struct {
	// Ticket 票据串。
	Ticket string `json:"ticket"`
	// ExpiresAt 票据失效时间（Unix 毫秒）。
	ExpiresAt int64 `json:"expires_at"`
}

// ---------------------------------------------------------------------------
// GET /api/v1/rankings/boards
// ---------------------------------------------------------------------------

// RankingBoard 描述一个官方榜单。
type RankingBoard struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      Source `json:"source"`
}

// RankingBoardsData 是榜单列表响应。
type RankingBoardsData struct {
	Items []RankingBoard `json:"items"`
}

// ---------------------------------------------------------------------------
// GET /api/v1/feed
// ---------------------------------------------------------------------------

// FeedData 是首页推荐流响应。
type FeedData struct {
	Page      int             `json:"page"`
	PageCount int             `json:"page_count"`
	HasMore   bool            `json:"has_more"`
	Scope     string          `json:"scope"`
	Total     int             `json:"total"`
	List      []SeriesSummary `json:"list"`
}

// ---------------------------------------------------------------------------
// 兼容别名：早期文档使用的类型名
// ---------------------------------------------------------------------------

// CatalogItem 是 SeriesSummary 的旧名。
type CatalogItem = SeriesSummary

// Episode 是 EpisodeInfo 的旧名。
type Episode = EpisodeInfo

// CatalogResponse 是 CatalogData 的旧名。
type CatalogResponse = CatalogData

// SeriesResponse 是 SeriesData 的旧名。
type SeriesResponse = SeriesData

// SearchResponse 是 SearchData 的旧名。
type SearchResponse = SearchData

// ResolveResponse 是 ResolveData 的旧名。
type ResolveResponse = ResolveData
