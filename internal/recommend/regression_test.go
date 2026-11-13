package recommend

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestTrainRegressionLearnsContrastingTags(t *testing.T) {
	examples := make([]RegressionExample, 0, 80)
	for i := 0; i < 40; i++ {
		examples = append(examples,
			RegressionExample{Tags: []string{"bright"}, Sources: []string{SourceRecommendationFeed}, Liked: true, Weight: 1},
			RegressionExample{Tags: []string{"dark"}, Sources: []string{SourceRecommendationFeed}, Liked: false, Weight: 1},
		)
	}
	model := TrainRegression(examples)
	if !model.Trained || model.Version != "logistic-v1" || model.Samples != 80 || model.Positives != 40 || model.Negatives != 40 {
		t.Fatalf("model metadata = %+v", model)
	}
	liked := model.Predict([]string{" BRIGHT "}, []string{SourceRecommendationFeed})
	disliked := model.Predict([]string{"dark"}, []string{SourceRecommendationFeed})
	if liked < 0.6 || disliked > 0.4 || liked <= disliked {
		t.Fatalf("predictions did not separate tags: liked=%g disliked=%g", liked, disliked)
	}
	for _, coefficient := range model.Coefficients {
		if math.IsNaN(coefficient) || math.IsInf(coefficient, 0) {
			t.Fatalf("non-finite coefficient %g", coefficient)
		}
	}
}

func TestTrainRegressionNormalizesDuplicatesAndIsOrderIndependent(t *testing.T) {
	base := []RegressionExample{
		{Tags: []string{" Blue ", "blue", "BLUE"}, Sources: []string{" recommendation_feed ", SourceRecommendationFeed}, Liked: true, Weight: 1},
		{Tags: []string{"red"}, Sources: []string{SourceTagSearch}, Liked: false, Weight: 1},
	}
	deduplicated := []RegressionExample{
		{Tags: []string{"blue"}, Sources: []string{SourceRecommendationFeed}, Liked: true, Weight: 1},
		{Tags: []string{"red"}, Sources: []string{SourceTagSearch}, Liked: false, Weight: 1},
	}
	first, second := TrainRegression(base), TrainRegression(deduplicated)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("duplicate/case variants changed the fitted model")
	}
	reversed := TrainRegression([]RegressionExample{deduplicated[1], deduplicated[0]})
	if !reflect.DeepEqual(first, reversed) {
		t.Fatal("input order changed the fitted model")
	}
}

func TestTrainRegressionBalancesClassMass(t *testing.T) {
	examples := make([]RegressionExample, 0, 201)
	for i := 0; i < 200; i++ {
		examples = append(examples, RegressionExample{Tags: []string{"same"}, Sources: []string{SourceRelatedArtist}, Liked: true, Weight: 1})
	}
	examples = append(examples, RegressionExample{Tags: []string{"same"}, Sources: []string{SourceRelatedArtist}, Liked: false, Weight: 1})
	model := TrainRegression(examples)
	prediction := model.Predict([]string{"same"}, []string{SourceRelatedArtist})
	if !model.Trained || math.Abs(prediction-0.5) > 0.01 {
		t.Fatalf("unbalanced labels overbiased prediction: trained=%v prediction=%g", model.Trained, prediction)
	}
}

func TestTrainRegressionColdStartAndInvalidWeights(t *testing.T) {
	singleClass := TrainRegression([]RegressionExample{
		{Tags: []string{"liked"}, Liked: true, Weight: 1},
		{Tags: []string{"other"}, Liked: true, Weight: 0.2},
	})
	if singleClass.Trained || singleClass.Samples != 2 || singleClass.Positives != 2 || singleClass.Negatives != 0 || singleClass.Predict(nil, nil) != 0.5 {
		t.Fatalf("single-class model = %+v", singleClass)
	}
	model := TrainRegression([]RegressionExample{
		{Tags: []string{"yes"}, Liked: true, Weight: 1},
		{Tags: []string{"no"}, Liked: false, Weight: 1},
		{Liked: true, Weight: 0},
		{Liked: false, Weight: -2},
		{Liked: true, Weight: math.NaN()},
		{Liked: false, Weight: math.Inf(1)},
	})
	if !model.Trained || model.Samples != 2 || model.Positives != 1 || model.Negatives != 1 {
		t.Fatalf("invalid-weight examples were not ignored: %+v", model)
	}
}

func TestTrainRegressionDoesNotModifyInputsAndSurvivesJSONRoundTrip(t *testing.T) {
	examples := []RegressionExample{
		{Tags: []string{" A ", "a", "画师"}, Sources: []string{SourceRelatedArtwork}, Liked: true, Weight: 0.2},
		{Tags: []string{"B"}, Sources: []string{SourceTagSearch}, Liked: false, Weight: 1},
	}
	snapshot := make([]RegressionExample, len(examples))
	copy(snapshot, examples)
	for i := range examples {
		snapshot[i].Tags = append([]string(nil), examples[i].Tags...)
		snapshot[i].Sources = append([]string(nil), examples[i].Sources...)
	}
	model := TrainRegression(examples)
	if !reflect.DeepEqual(examples, snapshot) {
		t.Fatal("training modified input examples")
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RegressionModel
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct{ tags, sources []string }{
		{[]string{"a"}, []string{SourceRelatedArtwork}},
		{[]string{"b", "missing"}, []string{SourceTagSearch}},
	} {
		if got, want := decoded.Predict(input.tags, input.sources), model.Predict(input.tags, input.sources); got != want {
			t.Fatalf("round-trip prediction=%g want=%g", got, want)
		}
	}
	if again, err := json.Marshal(TrainRegression(examples)); err != nil || string(again) != string(encoded) {
		t.Fatalf("training/JSON output not deterministic: %v", err)
	}
}

func TestRegressionPredictMalformedModelRemainsFiniteAndBounded(t *testing.T) {
	models := []RegressionModel{
		{Trained: false, Intercept: math.NaN(), Coefficients: []float64{math.Inf(1)}},
		{Trained: true, Intercept: math.NaN(), Coefficients: []float64{math.NaN(), math.Inf(-1)}},
		{Trained: true, Intercept: math.MaxFloat64, Coefficients: []float64{math.MaxFloat64}},
		{Trained: true, Intercept: -math.MaxFloat64, Coefficients: []float64{math.MaxFloat64}},
	}
	for _, model := range models {
		prediction := model.Predict([]string{"anything"}, nil)
		if math.IsNaN(prediction) || math.IsInf(prediction, 0) || prediction < 0 || prediction > 1 {
			t.Fatalf("malformed model prediction=%g for %+v", prediction, model)
		}
	}
}
