package adapter

import (
	"context"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/ststyin/torrvia/internal/config"
	"github.com/ststyin/torrvia/internal/model"
)

// mock provides deterministic demo data for Web, REST, and MCP flows without real tracker credentials.
type mock struct {
	site config.Site
}

func newMock(site config.Site) *mock { return &mock{site: site} }

func (m *mock) ID() string   { return m.site.ID }
func (m *mock) Name() string { return m.site.Name }
func (m *mock) Type() string { return "mock" }

func (m *mock) Health(context.Context) error { return nil }

type mockEntry struct {
	title   string
	aliases []string
	sizeGB  float64
	seeders int
}

// Release-name variations exercise cross-site deduplication.
var mockCatalog = []mockEntry{
	{"Interstellar.2014.2160p.UHD.BluRay.REMUX.HDR.HEVC.DTS-HD.MA.5.1-FGT", []string{"星际穿越", "interstellar"}, 62.4, 48},
	{"星际穿越.Interstellar.2014.1080p.BluRay.x264.DTS-WiKi", []string{"星际穿越", "interstellar"}, 12.8, 126},
	{"Oppenheimer.2023.2160p.WEB-DL.DDP5.1.Atmos.H.265-FLUX", []string{"奥本海默", "oppenheimer"}, 28.6, 87},
	{"奥本海默.Oppenheimer.2023.1080p.BluRay.x264.DTS-HD.MA.5.1", []string{"奥本海默", "oppenheimer"}, 15.2, 203},
	{"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.DV.HDR.HEVC.TrueHD.7.1.Atmos", []string{"沙丘2", "沙丘", "dune"}, 74.1, 61},
	{"沙丘2.Dune.Part.Two.2024.1080p.BluRay.x264.DTS-HD.MA.5.1", []string{"沙丘2", "沙丘", "dune"}, 18.3, 174},
	{"Spirited.Away.2001.2160p.UHD.BluRay.REMUX.HDR.HEVC.FLAC.2.0", []string{"千与千寻", "spirited away"}, 55.7, 39},
	{"千与千寻.Spirited.Away.2001.1080p.BluRay.x264.AC3", []string{"千与千寻", "spirited away"}, 9.6, 231},
	{"The.Dark.Knight.2008.1080p.BluRay.x264.DTS-WiKi", []string{"黑暗骑士", "蝙蝠侠", "dark knight"}, 13.1, 158},
	{"蝙蝠侠：黑暗骑士.The.Dark.Knight.2008.2160p.UHD.BluRay.REMUX.HDR", []string{"黑暗骑士", "蝙蝠侠", "dark knight"}, 58.9, 44},
	{"流浪地球2.The.Wandering.Earth.II.2023.2160p.WEB-DL.H265.DDP5.1", []string{"流浪地球", "wandering earth"}, 32.7, 96},
	{"让子弹飞.Let.the.Bullets.Fly.2010.1080p.BluRay.x264.DTS", []string{"让子弹飞", "bullets fly"}, 11.4, 187},
	{"Blade.Runner.2049.2017.2160p.UHD.BluRay.REMUX.HDR.HEVC.DTS-HD.MA.7.1", []string{"银翼杀手", "blade runner"}, 68.2, 52},
	{"银翼杀手2049.Blade.Runner.2049.2017.1080p.BluRay.x264.DTS", []string{"银翼杀手", "blade runner"}, 14.7, 143},
	{"Inception.2010.1080p.BluRay.x264.DTS-WiKi", []string{"盗梦空间", "inception"}, 10.9, 212},
	{"盗梦空间.Inception.2010.2160p.UHD.BluRay.REMUX.HDR.HEVC", []string{"盗梦空间", "inception"}, 57.3, 47},
	{"Mad.Max.Fury.Road.2015.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.7.1", []string{"疯狂的麦克斯", "mad max"}, 61.8, 35},
	{"疯狂的麦克斯4.Mad.Max.Fury.Road.2015.1080p.BluRay.x264.DTS", []string{"疯狂的麦克斯", "mad max"}, 12.2, 118},
	{"The.Grand.Budapest.Hotel.2014.1080p.BluRay.x264.DTS", []string{"布达佩斯大饭店", "budapest"}, 9.8, 94},
	{"寄生虫.Parasite.2019.1080p.BluRay.x264.DTS-HD.MA.5.1", []string{"寄生虫", "parasite"}, 11.6, 167},
	{"Whiplash.2014.2160p.UHD.BluRay.REMUX.HDR.HEVC.DTS-HD.MA.5.1", []string{"爆裂鼓手", "whiplash"}, 48.3, 41},
	{"爆裂鼓手.Whiplash.2014.1080p.BluRay.x264.DTS", []string{"爆裂鼓手", "whiplash"}, 10.4, 132},
}

// promoPool gives demo sites different promotion distributions.
var promoPool = []model.Promo{
	model.PromoFree, model.Promo2xFree, model.Promo2x,
	model.PromoNone, model.Promo50, model.PromoNone,
	model.Promo2x, model.PromoFree, model.Promo30, model.PromoNone,
}

func hashOf(parts ...string) uint32 {
	h := fnv.New32a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
	}
	return h.Sum32()
}

func (m *mock) Search(_ context.Context, q model.Query) ([]model.Torrent, error) {
	kw := model.NormalizeTitle(q.Keyword)
	now := time.Now()
	out := make([]model.Torrent, 0, len(mockCatalog))

	for i, e := range mockCatalog {
		if kw != "" && !mockMatch(kw, e) {
			continue
		}
		h := hashOf(m.site.ID, e.title)
		id := strconv.FormatUint(uint64(h), 10)

		// Vary peer counts between demo sites.
		scale := 1.0 + float64(int(h%7)-3)/10.0
		seeders := int(float64(e.seeders)*scale) + int(h%9) - 4
		if seeders < 0 {
			seeders = 0
		}
		promo := promoPool[(int(h)+i)%len(promoPool)]

		sizeJitter := 1.0 + float64(int(h%5)-2)/200.0 // Vary sizes by +/-1% between sites

		t := model.Torrent{
			ID:          m.site.ID + ":" + id,
			Site:        m.site.ID,
			SiteName:    m.site.Name,
			Title:       e.title,
			SizeBytes:   int64(e.sizeGB * sizeJitter * 1024 * 1024 * 1024),
			Seeders:     seeders,
			Leechers:    int(h % 17),
			Snatched:    int(h % 300),
			Promo:       promo,
			Resolution:  model.DetectResolution(e.title),
			PublishTime: now.Add(-time.Duration(int(h%240)+1) * time.Hour).Format(time.RFC3339),
			Magnet:      "magnet:?xt=urn:btih:" + id + id + "&dn=" + e.title,
		}
		// Demo sites have no real detail URL; leave the link empty.
		if m.site.BaseURL != "" {
			t.DetailURL = m.site.BaseURL + "/details.php?id=" + id
			t.DownloadURL = m.site.BaseURL + "/download.php?id=" + id
		}
		t.Recompute()
		out = append(out, t)
	}
	return out, nil
}

func mockMatch(kw string, e mockEntry) bool {
	if strings.Contains(model.NormalizeTitle(e.title), kw) {
		return true
	}
	for _, a := range e.aliases {
		if strings.Contains(model.NormalizeTitle(a), kw) {
			return true
		}
	}
	return false
}
