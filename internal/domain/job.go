package domain

import "encoding/json"

type Job struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"`
	Key             string          `json:"-"`
	Payload         json.RawMessage `json:"-"`
	State           string          `json:"state"`
	Attempt         int             `json:"attempt"`
	AvailableAt     int64           `json:"available_at"`
	LeaseToken      string          `json:"-"`
	LeaseUntil      int64           `json:"-"`
	CancelRequested bool            `json:"cancel_requested"`
	ProgressPages   int64           `json:"progress_pages"`
	ProgressItems   int64           `json:"progress_items"`
	CreatedAt       int64           `json:"created_at"`
	UpdatedAt       int64           `json:"updated_at"`
	ErrorCode       string          `json:"error_code"`
}
