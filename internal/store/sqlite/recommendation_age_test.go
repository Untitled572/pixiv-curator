package sqlite

import "testing"

func TestRecommendationAgeClassThresholds(t *testing.T) {
	tests := []struct {
		ratings []string
		want    string
	}{
		{[]string{"safe", "safe", "safe", "safe", "safe", "safe", "safe"}, "unknown"},
		{[]string{"safe", "safe", "safe", "safe", "safe", "safe", "safe", "safe"}, "safe"},
		{[]string{"r18", "safe", "safe", "safe", "safe", "safe", "safe", "safe", "safe", "safe"}, "mixed"},
		{[]string{"r18", "r18", "r18g", "safe", "safe", "safe", "safe", "safe", "safe", "safe"}, "adult"},
		{[]string{"safe", "safe", "safe", "safe", "safe", "safe", "safe", "safe", "unknown", "unknown", "unknown"}, "unknown"},
	}
	for _, tc := range tests {
		if got := recommendationAgeClass(tc.ratings); got != tc.want {
			t.Fatalf("%v: got %s want %s", tc.ratings, got, tc.want)
		}
	}
}
func TestRecommendationV2ThreeClassQuotasAndAdultCap(t *testing.T) {
	candidates := []recommendationMixCandidate{}
	for _, group := range []struct {
		name  string
		start int64
	}{{"safe", 100}, {"mixed", 200}, {"adult", 300}, {"unknown", 400}} {
		for i := int64(0); i < 10; i++ {
			candidates = append(candidates, recommendationMixCandidate{artistID: group.start + i, class: group.name, weight: 1})
		}
	}
	selected := selectRecommendationMixV2(candidates, 9, map[string]float64{"safe": 5, "mixed": 2, "adult": 2})
	counts := map[string]int{}
	for _, c := range selected {
		counts[c.class]++
	}
	if len(selected) != 9 || counts["safe"] != 4 || counts["mixed"] != 2 || counts["adult"] != 2 || counts["unknown"] != 1 {
		t.Fatalf("counts %v", counts)
	}
	onlyAdult := []recommendationMixCandidate{}
	for _, c := range candidates {
		if c.class == "adult" {
			onlyAdult = append(onlyAdult, c)
		}
	}
	selected = selectRecommendationMixV2(onlyAdult, 9, map[string]float64{"safe": 8, "mixed": 0, "adult": 1})
	if len(selected) > 2 {
		t.Fatalf("adult overflow %d", len(selected))
	}
}
func TestRecommendationV2RelatedTargetsUseLargestRemainder(t *testing.T) {
	candidates := []recommendationMixCandidate{}
	for _, g := range []struct {
		name string
		base int64
	}{{"safe", 1000}, {"mixed", 2000}, {"adult", 3000}} {
		for i := int64(0); i < 20; i++ {
			c := recommendationMixCandidate{artistID: g.base + i, class: g.name, weight: 1}
			if i < 10 {
				c.sources = []string{"related_artist"}
			}
			candidates = append(candidates, c)
		}
	}
	selected := selectRecommendationMixV2(candidates, 9, map[string]float64{"safe": 5, "mixed": 2, "adult": 2})
	related := map[string]int{}
	for _, c := range selected {
		if hasRecommendationMixSource(c.sources, "related_artist") {
			related[c.class]++
		}
	}
	if related["safe"] != 3 || related["mixed"] != 1 || related["adult"] != 1 {
		t.Fatalf("related quotas %v", related)
	}
}
