package domain

// CatalogStats reports local catalog row counts. Image pages count stored
// pagination rows and do not imply that an image is eligible for selection.
type CatalogStats struct {
	Accounts                int64   `json:"accounts"`
	Artists                 int64   `json:"artists"`
	LibraryArtists          int64   `json:"library_artists"`
	BlockedArtists          int64   `json:"blocked_artists"`
	CollectingArtists       int64   `json:"collecting_artists"`
	Artworks                int64   `json:"artworks"`
	ImagePages              int64   `json:"image_pages"`
	LandscapePages          int64   `json:"landscape_pages"`
	PortraitPages           int64   `json:"portrait_pages"`
	SquarePages             int64   `json:"square_pages"`
	UnknownOrientationPages int64   `json:"unknown_orientation_pages"`
	R18ImagePages           int64   `json:"r18_image_pages"`
	R18ImageRatio           float64 `json:"r18_image_ratio"`
	EligibleImagePages      int64   `json:"eligible_image_pages"`
	IndexedPages            int64   `json:"indexed_pages"`
	UnavailablePages        int64   `json:"unavailable_pages"`
	UnknownRatingArtworks   int64   `json:"unknown_rating_artworks"`
	UnknownAIArtworks       int64   `json:"unknown_ai_artworks"`
}

type LibraryCleanupItem struct {
	Artist          Artist                 `json:"artist"`
	SampleIllustIDs []string               `json:"sample_illust_ids"`
	Samples         []LibraryCleanupSample `json:"samples"`
	Trimmed         bool                   `json:"trimmed"`
	SlotType        string                 `json:"slot_type"`
	AuditReason     string                 `json:"audit_reason"`
	Summary         LibraryCleanupSummary  `json:"summary"`
}

// LibraryCleanupSample identifies the exact preview page selected for review.
type LibraryCleanupSample struct {
	IllustID    string `json:"illust_id"`
	PageIndex   int    `json:"page_index"`
	PageCount   int    `json:"page_count,omitempty"`
	PageIndices []int  `json:"page_indices,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type LibraryCleanupSummary struct {
	PagesTrimmed    bool  `json:"pages_trimmed"`
	Artworks        int64 `json:"artworks"`
	Pages           int64 `json:"pages"`
	EligiblePages   int64 `json:"eligible_pages"`
	R18             int64 `json:"r18"`
	KnownRating     int64 `json:"known_rating"`
	AI              int64 `json:"ai"`
	KnownAI         int64 `json:"known_ai"`
	Likes           int64 `json:"likes"`
	Dislikes        int64 `json:"dislikes"`
	LatestPublished int64 `json:"latest_published_at,omitempty"`
}
