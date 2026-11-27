package domain

const DefaultCollectionIntervalHours = 24

type CollectionSchedule struct {
	IntervalHours int `json:"interval_hours"`
}

func (s CollectionSchedule) Valid() bool {
	return s.IntervalHours >= 1 && s.IntervalHours <= 365*24
}
