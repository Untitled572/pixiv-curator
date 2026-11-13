// Package feedback converts per-page preference counts into a conservative
// sampling weight adjustment.
package feedback

import (
	"math"
	"strings"
)

// LogBias averages signed counts across distinct normalized tags and caps the
// resulting log-odds adjustment so one feedback action cannot dominate.
func LogBias(counts map[string]float64, tags []string) float64 {
	unique := make(map[string]struct{}, len(tags))
	// Store aggregation already normalizes keys. Normalize caller-provided keys
	// only once, rather than scanning the entire count map for every input tag.
	normalized := make(map[string]float64, len(counts))
	for tag, count := range counts {
		normalized[strings.ToLower(strings.TrimSpace(tag))] += count
	}
	var total float64
	for _, tag := range tags {
		key := strings.ToLower(strings.TrimSpace(tag))
		if key == "" {
			continue
		}
		if _, exists := unique[key]; exists {
			continue
		}
		unique[key] = struct{}{}
		total += normalized[key]
	}
	if len(unique) == 0 {
		return 0
	}
	return math.Max(-0.4, math.Min(0.4, 0.004*total/float64(len(unique))))
}

// Weight maps the bounded log-odds bias to a multiplicative factor.
func Weight(counts map[string]float64, tags []string) float64 { return math.Exp(LogBias(counts, tags)) }
