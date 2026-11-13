package domain

import "encoding/json"

type RecommendationRun struct {
	ProgressPercent int             `json:"progress_percent"`
	ID              string          `json:"id"`
	State           string          `json:"state"`
	SeedCursor      int64           `json:"-"`
	CandidateCursor int64           `json:"-"`
	RequestCount    int             `json:"request_count"`
	Budget          json.RawMessage `json:"budget"`
}
type Recommendation struct {
	Exploration    bool            `json:"exploration"`
	RunID          string          `json:"run_id"`
	ArtistID       int64           `json:"artist_id,string"`
	Artist         Artist          `json:"artist"`
	Score          float64         `json:"score"`
	Reasons        []string        `json:"reasons"`
	Sources        []string        `json:"sources"`
	SampleIDs      []string        `json:"sample_illust_ids"`
	Confidence     string          `json:"confidence"`
	Version        string          `json:"score_version"`
	Components     json.RawMessage `json:"components"`
	ContentRating  string          `json:"content_rating"`
	AdultCount     int             `json:"adult_count"`
	SafeCount      int             `json:"safe_count"`
	UnknownCount   int             `json:"unknown_count"`
	AIStatus       string          `json:"ai_status"`
	AICount        int             `json:"ai_count"`
	NonAICount     int             `json:"non_ai_count"`
	UnknownAICount int             `json:"unknown_ai_count"`
}
