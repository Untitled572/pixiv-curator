package pixiv

import (
	"fmt"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

// RawArtist is the portion of a Pixiv App API user object used by the catalog.
type RawArtist struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Account          string `json:"account"`
	ProfileImageURLs struct {
		Medium string `json:"medium"`
	} `json:"profile_image_urls"`
}

// RawArtwork is the catalog-relevant portion of an App API illustration object.
type RawArtwork struct {
	ID             int64     `json:"id"`
	User           RawArtist `json:"user"`
	Title          string    `json:"title"`
	Caption        string    `json:"caption"`
	Type           string    `json:"type"`
	PageCount      int       `json:"page_count"`
	Width          int       `json:"width"`
	Height         int       `json:"height"`
	TotalBookmarks *int64    `json:"total_bookmarks"`
	TotalView      *int64    `json:"total_view"`
	CreateDate     string    `json:"create_date"`
	XRestrict      *int      `json:"x_restrict"`
	IllustAIType   *int      `json:"illust_ai_type"`
	Tags           []struct {
		Name string `json:"name"`
	} `json:"tags"`
	ImageURLs struct {
		Medium string `json:"medium"`
	} `json:"image_urls"`
	MetaSinglePage struct {
		OriginalImageURL string `json:"original_image_url"`
	} `json:"meta_single_page"`
	MetaPages []struct {
		ImageURLs struct {
			Original string `json:"original"`
			Medium   string `json:"medium"`
		} `json:"image_urls"`
	} `json:"meta_pages"`
}

const (
	maxArtworkPages = 1000
	maxArtworkTags  = 1000
)

// NormalizeArtist maps a Pixiv user object to a pending local artist.
func NormalizeArtist(raw RawArtist) (domain.Artist, error) {
	if raw.ID <= 0 {
		return domain.Artist{}, domain.ErrInvalid
	}
	if raw.ProfileImageURLs.Medium != "" && !domain.ValidAvatarURL(raw.ProfileImageURLs.Medium) {
		return domain.Artist{}, domain.ErrInvalid
	}
	return domain.Artist{
		ID: raw.ID, Name: raw.Name, Account: raw.Account,
		AvatarURL:    raw.ProfileImageURLs.Medium,
		ReviewStatus: "pending", CollectionEnabled: false, RemoteFollowed: false,
	}, nil
}

// NormalizeArtwork maps an App API illustration to catalog metadata. Missing
// or unsupported page originals leave metadata pending so they cannot qualify
// as a complete catalog record.
func NormalizeArtwork(raw RawArtwork) (domain.Artwork, error) {
	if raw.ID <= 0 || raw.User.ID <= 0 || raw.PageCount < 0 || raw.PageCount > maxArtworkPages || len(raw.Tags) > maxArtworkTags || raw.Width < 0 || raw.Height < 0 {
		return domain.Artwork{}, domain.ErrInvalid
	}

	kind := "unknown"
	switch raw.Type {
	case "illust", "manga", "ugoira":
		kind = raw.Type
	}
	rating := "unknown"
	if raw.XRestrict != nil {
		switch *raw.XRestrict {
		case 0:
			rating = "safe"
		case 1:
			rating = "r18"
		case 2:
			rating = "r18g"
		}
	}
	aiStatus := "unknown"
	if raw.IllustAIType != nil {
		switch *raw.IllustAIType {
		case 1:
			aiStatus = "non_ai"
		case 2:
			aiStatus = "ai"
		}
	}

	artwork := domain.Artwork{
		ID: raw.ID, ArtistID: raw.User.ID, Title: raw.Title, Caption: raw.Caption,
		Kind: kind, Rating: rating, AIStatus: aiStatus, PageCount: raw.PageCount,
		Bookmarks: raw.TotalBookmarks, Views: raw.TotalView,
		MetadataState: "pending", Visible: true, Tags: make([]string, 0, len(raw.Tags)),
		Pages: make([]domain.Page, 0, raw.PageCount),
	}
	if publishedAt, err := time.Parse(time.RFC3339Nano, raw.CreateDate); err == nil {
		publishedMillis := publishedAt.UnixMilli()
		artwork.PublishedAt = &publishedMillis
	}
	for _, tag := range raw.Tags {
		artwork.Tags = append(artwork.Tags, tag.Name)
	}

	urls := make([]struct{ original, preview string }, raw.PageCount)
	if kind != "ugoira" {
		if raw.PageCount == 1 {
			urls[0].original = raw.MetaSinglePage.OriginalImageURL
			urls[0].preview = raw.ImageURLs.Medium
		} else if raw.PageCount > 1 {
			for i := 0; i < raw.PageCount && i < len(raw.MetaPages); i++ {
				urls[i].original = raw.MetaPages[i].ImageURLs.Original
				urls[i].preview = raw.MetaPages[i].ImageURLs.Medium
				if i == 0 && urls[i].preview == "" {
					urls[i].preview = raw.ImageURLs.Medium
				}
			}
		}
	}
	complete := raw.PageCount > 0 && kind != "ugoira" && len(urls) == raw.PageCount
	for i, item := range urls {
		if item.original != "" && !domain.ValidImageURL(item.original) {
			return domain.Artwork{}, fmt.Errorf("page %d original URL: %w", i, domain.ErrInvalid)
		}
		if item.preview != "" && !domain.ValidImageURL(item.preview) {
			return domain.Artwork{}, fmt.Errorf("page %d preview URL: %w", i, domain.ErrInvalid)
		}
		if item.original == "" {
			complete = false
		}
		pageState := "unavailable"
		if item.original != "" {
			pageState = "indexed"
		}
		width, height := raw.Width, raw.Height
		if i > 0 {
			// The Pixiv artwork dimensions describe its first image only. The
			// API meta_pages entries provide URLs but no independent dimensions.
			width, height = 0, 0
		}
		artwork.Pages = append(artwork.Pages, domain.Page{
			Index: i, OriginalURL: item.original, PreviewURL: item.preview,
			Width: width, Height: height, State: pageState,
		})
	}
	if complete {
		artwork.MetadataState = "complete"
	}
	return artwork, nil
}
