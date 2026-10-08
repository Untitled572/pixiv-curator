package domain

import "encoding/json"

type RecommendationRun struct {
	ID              string          `json:"id"`
	State           string          `json:"state"`
	SeedCursor      int64           `json:"-"`
	CandidateCursor int64           `json:"-"`
	RequestCount    int             `json:"request_count"`
	Budget          json.RawMessage `json:"budget"`
}
type Recommendation struct {
	RunID      string          `json:"run_id"`
	ArtistID   int64           `json:"artist_id,string"`
	Artist     Artist          `json:"artist"`
	Score      float64         `json:"score"`
	Reasons    []string        `json:"reasons"`
	Sources    []string        `json:"sources"`
	SampleIDs  []string        `json:"sample_illust_ids"`
	Confidence string          `json:"confidence"`
	Version    string          `json:"score_version"`
	Components json.RawMessage `json:"components"`
}
