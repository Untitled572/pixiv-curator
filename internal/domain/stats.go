package domain

// CatalogStats reports local catalog row counts. Image pages count stored
// pagination rows and do not imply that an image is eligible for selection.
type CatalogStats struct {
	Accounts              int64 `json:"accounts"`
	Artists               int64 `json:"artists"`
	LibraryArtists        int64 `json:"library_artists"`
	BlockedArtists        int64 `json:"blocked_artists"`
	CollectingArtists     int64 `json:"collecting_artists"`
	Artworks              int64 `json:"artworks"`
	ImagePages            int64 `json:"image_pages"`
	IndexedPages          int64 `json:"indexed_pages"`
	UnavailablePages      int64 `json:"unavailable_pages"`
	UnknownRatingArtworks int64 `json:"unknown_rating_artworks"`
	UnknownAIArtworks     int64 `json:"unknown_ai_artworks"`
}
