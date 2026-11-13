package domain

// DistributionHistoryItem represents a recorded image selection. Its URL is
// constructed from the fixed artwork/page identity, never copied from storage.
type DistributionHistoryItem struct {
	Rating       string   `json:"rating"`
	AIStatus     string   `json:"ai_status"`
	Tags         []string `json:"tags"`
	ID           string   `json:"id"`
	SelectedAt   int64    `json:"selected_at"`
	IllustID     int64    `json:"illust_id,string"`
	PageIndex    int      `json:"page_index"`
	ArtistID     int64    `json:"artist_id,string"`
	ArtistName   string   `json:"artist_name"`
	Title        string   `json:"title"`
	Width        int      `json:"width"`
	Height       int      `json:"height"`
	Available    bool     `json:"available"`
	Feedback     string   `json:"feedback"`
	ThumbnailURL string   `json:"thumbnail_url"`
	FullURL      string   `json:"full_url"`
}

// DistributionHistoryPage is one bounded page of recorded selections.
type DistributionHistoryPage struct {
	Items      []DistributionHistoryItem `json:"items"`
	Total      int64                     `json:"total"`
	Page       int                       `json:"page"`
	PageSize   int                       `json:"page_size"`
	TotalPages int64                     `json:"total_pages"`
}
