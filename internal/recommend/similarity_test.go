package recommend

import (
	"math"
	"testing"
)

func TestSimilarityWeakCenterRequiresAndCannotDominateExplicitNegative(t *testing.T) {
	positives := []SimilarityExample{{Tags: []string{"cat", "green"}, Weight: 1}, {Tags: []string{"cat", "blue"}, Weight: 1}}
	weak := []SimilarityExample{}
	for i := 0; i < 150; i++ {
		weak = append(weak, SimilarityExample{Tags: []string{"dog", "blue"}, Weak: true, Weight: 1})
	}
	without := BuildSimilarityModel(append(append([]SimilarityExample{}, positives...), weak...))
	if without.WeakFactor != 0 || without.NegativeFactor != 0 {
		t.Fatalf("weak without explicit negative: %+v", without)
	}
	examples := append(append([]SimilarityExample{}, positives...), SimilarityExample{Tags: []string{"dog", "blue"}, Negative: true, Weight: 1})
	with := BuildSimilarityModel(append(examples, weak...))
	if with.NegativeFactor <= 0 || with.WeakFactor > with.NegativeFactor*0.25 {
		t.Fatalf("negative=%f weak=%f", with.NegativeFactor, with.WeakFactor)
	}
	if !(with.Score([]string{"cat", "green"}, 0) > with.Score([]string{"dog", "blue"}, 0)) {
		t.Fatal("explicit preference did not affect score")
	}
	if with.Score([]string{"cat", "green"}, 3) <= with.Score([]string{"cat", "green"}, 0) {
		t.Fatal("different seed hits should contribute")
	}
	if math.IsNaN(with.Score([]string{"unseen"}, 0)) {
		t.Fatal("unknown vector returned NaN")
	}
}
func TestSimilarityVocabularyExcludesBookmarkTags(t *testing.T) {
	m := BuildSimilarityModel([]SimilarityExample{{Tags: []string{"cat", "1000users入り", "bookmark count", "ブックマーク"}, Weight: 1}})
	if _, ok := m.IDF["1000users入り"]; ok {
		t.Fatal("bookmark count tag retained")
	}
	if _, ok := m.IDF["bookmark count"]; ok {
		t.Fatal("bookmark tag retained")
	}
	if _, ok := m.IDF["ブックマーク"]; ok {
		t.Fatal("bookmark tag retained")
	}
}
