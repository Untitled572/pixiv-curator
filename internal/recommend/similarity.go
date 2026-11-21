package recommend

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

type SimilarityExample struct {
	Tags     []string `json:"tags"`
	Negative bool     `json:"negative"`
	Weak     bool     `json:"weak"`
	Weight   float64  `json:"weight"`
}
type SimilarityModel struct {
	IDF            map[string]float64 `json:"idf"`
	Positive       map[string]float64 `json:"positive"`
	Negative       map[string]float64 `json:"negative"`
	Weak           map[string]float64 `json:"weak"`
	NegativeFactor float64            `json:"negative_factor"`
	WeakFactor     float64            `json:"weak_factor"`
}

var bookmarkCountTag = regexp.MustCompile(`(?i)^\d+\s*(users?入り|bookmarks?|ブックマーク)`)

func similarityTags(tags []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range tags {
		tag := strings.ToLower(strings.TrimSpace(raw))
		if tag == "" || seen[tag] || strings.Contains(tag, "ブックマーク") || strings.Contains(tag, "bookmark") || bookmarkCountTag.MatchString(tag) {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}
func vector(tags []string, idf map[string]float64) map[string]float64 {
	out := map[string]float64{}
	var norm float64
	for _, tag := range similarityTags(tags) {
		if value := idf[tag]; value > 0 {
			out[tag] = value
			norm += value * value
		}
	}
	if norm > 0 {
		norm = math.Sqrt(norm)
		for tag := range out {
			out[tag] /= norm
		}
	}
	return out
}
func normalizeCenter(center map[string]float64) {
	var norm float64
	for _, v := range center {
		norm += v * v
	}
	if norm > 0 {
		norm = math.Sqrt(norm)
		for k := range center {
			center[k] /= norm
		}
	}
}
func BuildSimilarityModel(examples []SimilarityExample) SimilarityModel {
	m := SimilarityModel{IDF: map[string]float64{}, Positive: map[string]float64{}, Negative: map[string]float64{}, Weak: map[string]float64{}}
	df := map[string]int{}
	docs := 0
	for _, e := range examples {
		if e.Weight <= 0 || e.Weak {
			continue
		}
		tags := similarityTags(e.Tags)
		if len(tags) == 0 {
			continue
		}
		docs++
		for _, tag := range tags {
			df[tag]++
		}
	}
	for tag, n := range df {
		if docs >= 10 && n < 2 {
			continue
		}
		m.IDF[tag] = math.Log(float64(docs+1)/float64(n+1)) + 1
	}
	var negatives, weak float64
	for _, e := range examples {
		if e.Weight <= 0 {
			continue
		}
		v := vector(e.Tags, m.IDF)
		if len(v) == 0 {
			continue
		}
		center := m.Positive
		if e.Weak {
			center = m.Weak
			weak += e.Weight
		} else if e.Negative {
			center = m.Negative
			negatives += e.Weight
		}
		for k, value := range v {
			center[k] += value * e.Weight
		}
	}
	normalizeCenter(m.Positive)
	normalizeCenter(m.Negative)
	normalizeCenter(m.Weak)
	if negatives > 0 {
		m.NegativeFactor = 0.25 * negatives / (negatives + 10)
		m.WeakFactor = m.NegativeFactor * 0.25 * weak / (weak + 100)
	}
	return m
}
func dot(a, b map[string]float64) float64 {
	var sum float64
	for k, v := range a {
		sum += v * b[k]
	}
	return sum
}
func (m SimilarityModel) Score(tags []string, seedHits int) float64 {
	v := vector(tags, m.IDF)
	value := 0.5 + 0.4*dot(v, m.Positive) - m.NegativeFactor*dot(v, m.Negative) - m.WeakFactor*dot(v, m.Weak)
	if seedHits > 0 {
		value += 0.05 * (1 - math.Exp(-float64(seedHits)/2))
	}
	return math.Max(0, math.Min(1, value))
}
