package sqlite

import "testing"

func TestRecommendationV2AdultAndUnknownCaps(t *testing.T) {
	var candidates []recommendationMixCandidate
	for i := int64(1); i <= 20; i++ {
		candidates = append(candidates, recommendationMixCandidate{artistID: i, class: "adult", weight: 1})
	}
	for i := int64(21); i <= 25; i++ {
		candidates = append(candidates, recommendationMixCandidate{artistID: i, class: "unknown", weight: 1})
	}
	selected := selectRecommendationMixNormalVersion(candidates, 9, 0.25, nil, true)
	adult, unknown := 0, 0
	for _, c := range selected {
		if c.class == "adult" {
			adult++
		}
		if c.class == "unknown" {
			unknown++
		}
	}
	// One unknown slot leaves eight classified slots: adult target is two.
	if adult > 3 || unknown > 1 {
		t.Fatalf("adult=%d unknown=%d", adult, unknown)
	}
}
