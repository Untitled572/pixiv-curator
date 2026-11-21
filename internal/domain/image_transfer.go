package domain

// ImageTransfer describes a successfully served original image.
type ImageTransfer struct {
	SelectionID   string
	IllustID      int64
	PageIndex     int
	OriginalURL   string
	Width, Height int
	At            int64
}
