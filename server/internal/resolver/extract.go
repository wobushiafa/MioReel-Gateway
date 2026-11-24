package resolver

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ExtractedStream represents one decoded candidate stream returned by Extract (REF ln()).
type ExtractedStream struct {
	URL           string
	DecryptionKey string
	Codec         string
	VID           string
	ExpiresAt     int64
	MediaBytes    int64
	Quality       int
	QualityLabel  string
	Width         int
	Height        int
	CodecRank     int
}

var digitsPattern = regexp.MustCompile(`\d+`)

type rankedEntry struct {
	item      VideoItem
	codec     string
	codecRank int
	pixels    int
	width     int
	height    int
}

func defaultCodecRank(codec, vtype string) int {
	switch codec {
	case "h264", "avc", "avc1":
		return 0
	case "hevc", "h265", "hvc1", "hev1", "bytevc1":
		return 1
	default:
		if codec == "" {
			vt := strings.ToLower(strings.TrimSpace(vtype))
			if vt == "mp4" || vt == "mpeg" {
				return 2
			}
		}
		return 3
	}
}

func resolveDimensions(item VideoItem) (int, int) {
	pairs := [][2]int{
		{int(item.VWidth), int(item.VHeight)},
		{int(item.Width), int(item.Height)},
	}
	if item.VideoMeta != nil {
		pairs = append(pairs,
			[2]int{int(item.VideoMeta.VWidth), int(item.VideoMeta.VHeight)},
			[2]int{int(item.VideoMeta.Width), int(item.VideoMeta.Height)},
		)
	}
	for _, p := range pairs {
		if p[0] > 0 && p[1] > 0 {
			return p[0], p[1]
		}
	}
	return 0, 0
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// Extract implements ln(model, videoId, requestedQuality) from REF L1107-1119.
// requestedQuality == 0 corresponds to 'auto' (highest available).
func Extract(model *VideoModel, videoID string, requestedQuality int) ([]ExtractedStream, error) {
	if model == nil || len(model.VideoList) == 0 {
		return nil, &DomainError{Code: "MISSING_MODEL", Message: "video_list is empty"}
	}

	var entries []rankedEntry
	for _, item := range model.VideoList {
		hasURL := item.MainURL != "" || item.BackupURL != "" || len(item.BackupURLList) > 0
		if !hasURL {
			continue
		}
		metaCodec := ""
		metaDef := ""
		if item.VideoMeta != nil {
			metaCodec = item.VideoMeta.CodecType
			metaDef = item.VideoMeta.Definition
		}
		codec := strings.ToLower(firstNonEmpty(item.CodecType, metaCodec))
		rank := defaultCodecRank(codec, item.VType)
		if rank >= 3 {
			continue
		}
		w, h := resolveDimensions(item)
		pixels := 0
		if w > 0 && h > 0 {
			pixels = min(w, h)
		} else {
			label := firstNonEmpty(item.QualityDesc, item.Definition, metaDef)
			if m := digitsPattern.FindString(label); m != "" {
				pixels, _ = strconv.Atoi(m)
			}
		}
		if requestedQuality > 0 && (w <= 0 || h <= 0 || min(w, h) < requestedQuality) {
			// Match Di(value, downloadQuality) behavior only if caller requested strict floor;
			// note: when requestedQuality > 0 and no stream >= requestedQuality exists, fallback still handled by service if needed.
		}
		entries = append(entries, rankedEntry{
			item:      item,
			codec:     codec,
			codecRank: rank,
			pixels:    pixels,
			width:     w,
			height:    h,
		})
	}

	if len(entries) == 0 {
		return nil, &DomainError{Code: "NO_STREAM", Message: "no compatible H.264/HEVC stream found"}
	}

	if requestedQuality <= 0 {
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].pixels != entries[j].pixels {
				return entries[i].pixels > entries[j].pixels
			}
			return entries[i].codecRank < entries[j].codecRank
		})
	} else {
		sort.SliceStable(entries, func(i, j int) bool {
			li := min(entries[i].width, entries[i].height)
			rj := min(entries[j].width, entries[j].height)
			if li != rj {
				return li < rj
			}
			return entries[i].codecRank < entries[j].codecRank
		})
	}

	var keySeed []byte
	if model.KeySeed != "" {
		var err error
		keySeed, err = decodeBase64Flex(model.KeySeed)
		if err != nil {
			return nil, &DomainError{Code: "INVALID_RESPONSE", Message: "invalid key_seed base64"}
		}
	}

	expiresAt := AppModelExpiry(model, 0)
	seen := make(map[string]struct{})
	var out []ExtractedStream
	var lastErr error

	for _, c := range entries {
		spadeA := c.item.SpadeA
		if spadeA == "" && c.item.EncryptInfo != nil {
			spadeA = c.item.EncryptInfo.SpadeA
		}
		var contentKey string
		if spadeA != "" {
			var err error
			contentKey, err = DecodeSpadeKey(spadeA)
			if err != nil {
				lastErr = err
				continue
			}
		}

		candidates := make([]string, 0, 5+len(c.item.BackupURLList))
		candidates = append(candidates, c.item.MainURL, c.item.BackupURL, c.item.BackupURL1, c.item.BackupURL2, c.item.BackupURL3)
		candidates = append(candidates, c.item.BackupURLList...)

		for _, rawField := range candidates {
			if strings.TrimSpace(rawField) == "" {
				continue
			}
			var decodedURL string
			if len(keySeed) > 0 {
				var err error
				decodedURL, err = DecodeSpadeURL(rawField, keySeed)
				if err != nil {
					lastErr = err
					continue
				}
			} else {
				unescaped := UnescapeURL(rawField)
				if strings.HasPrefix(strings.ToLower(unescaped), "https://") {
					decodedURL = unescaped
				} else {
					b, err := decodeBase64Flex(rawField)
					if err != nil {
						lastErr = err
						continue
					}
					decodedURL = string(b)
				}
			}

			validURL, err := ValidateAllowedURL(decodedURL, MediaCDNDomains)
			if err != nil {
				lastErr = err
				continue
			}

			dedupKey := validURL + ":" + contentKey
			if _, exists := seen[dedupKey]; exists {
				continue
			}
			seen[dedupKey] = struct{}{}

			normCodec := c.codec
			if normCodec == "bytevc1" {
				normCodec = "hevc"
			} else if normCodec == "" {
				normCodec = "auto"
			}

			sizeBytes := int64(c.item.Size)
			if sizeBytes <= 0 && c.item.VideoMeta != nil {
				sizeBytes = int64(c.item.VideoMeta.Size)
			}
			if sizeBytes < 0 {
				sizeBytes = 0
			}

			qLabel := ""
			if c.pixels > 0 {
				qLabel = strconv.Itoa(c.pixels) + "p"
			}

			out = append(out, ExtractedStream{
				URL:           validURL,
				DecryptionKey: contentKey,
				Codec:         normCodec,
				VID:           videoID,
				ExpiresAt:     expiresAt,
				MediaBytes:    sizeBytes,
				Quality:       c.pixels,
				QualityLabel:  qLabel,
				Width:         c.width,
				Height:        c.height,
				CodecRank:     c.codecRank,
			})
		}
	}

	if len(out) > 0 {
		return out, nil
	}
	if de, ok := lastErr.(*DomainError); ok && de.Code == "SECURITY_BLOCKED" {
		return nil, de
	}
	return nil, &DomainError{Code: "INVALID_RESPONSE", Message: "unable to decode stream URL or key"}
}
