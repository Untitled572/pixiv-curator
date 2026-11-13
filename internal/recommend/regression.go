package recommend

import (
	"math"
	"sort"
)

const (
	RegressionVersion      = "logistic-v1"
	regressionFeatureCount = 512
	regressionSourceCount  = 4
	regressionIterations   = 180
	regressionLearningRate = 0.8
	regressionL2           = 0.12
)

// RegressionExample is one explicit user feedback label for a candidate.
// Liked=false represents an explicitly disliked candidate; callers should not
// manufacture examples from unreviewed recommendations.
type RegressionExample struct {
	Tags    []string
	Sources []string
	Liked   bool
	Weight  float64
}

// RegressionModel is a deterministic sparse logistic-regression model. The
// output is a ranking estimate, not a calibrated probability.
type RegressionModel struct {
	Version      string    `json:"version"`
	Coefficients []float64 `json:"coefficients"`
	Intercept    float64   `json:"intercept"`
	Samples      int       `json:"samples"`
	Positives    int       `json:"positives"`
	Negatives    int       `json:"negatives"`
	Trained      bool      `json:"trained"`
}

type regressionFeature struct {
	index int
	value float64
}

type regressionSample struct {
	features []regressionFeature
	liked    bool
	weight   float64
}

// TrainRegression fits a small deterministic L2-regularized logistic model.
// Only examples with finite positive weights participate. Positive and
// negative classes receive equal total training mass to reduce class imbalance.
func TrainRegression(examples []RegressionExample) RegressionModel {
	model := RegressionModel{
		Version:      RegressionVersion,
		Coefficients: make([]float64, regressionFeatureCount),
	}
	samples := make([]regressionSample, 0, len(examples))
	var positiveMax, negativeMax float64
	for _, example := range examples {
		if !finite(example.Weight) || example.Weight <= 0 {
			continue
		}
		sample := regressionSample{
			features: regressionFeatures(example.Tags, example.Sources),
			liked:    example.Liked,
			weight:   example.Weight,
		}
		samples = append(samples, sample)
		if sample.liked {
			model.Positives++
			if sample.weight > positiveMax {
				positiveMax = sample.weight
			}
		} else {
			model.Negatives++
			if sample.weight > negativeMax {
				negativeMax = sample.weight
			}
		}
	}
	model.Samples = len(samples)
	if model.Positives == 0 || model.Negatives == 0 {
		return model
	}
	model.Trained = true

	// Canonical ordering makes training independent of input order and map order.
	sort.Slice(samples, func(i, j int) bool { return regressionSampleLess(samples[i], samples[j]) })
	var positiveTotal, negativeTotal float64
	for _, sample := range samples {
		scaled := sample.weight
		if sample.liked {
			scaled /= positiveMax
			positiveTotal += scaled
		} else {
			scaled /= negativeMax
			negativeTotal += scaled
		}
	}
	if positiveTotal <= 0 || negativeTotal <= 0 || !finite(positiveTotal) || !finite(negativeTotal) {
		model.Trained = false
		return model
	}
	for i := range samples {
		if samples[i].liked {
			samples[i].weight = 0.5 * (samples[i].weight / positiveMax) / positiveTotal
		} else {
			samples[i].weight = 0.5 * (samples[i].weight / negativeMax) / negativeTotal
		}
	}

	gradient := make([]float64, regressionFeatureCount)
	for iteration := 0; iteration < regressionIterations; iteration++ {
		for i := range gradient {
			gradient[i] = regressionL2 * model.Coefficients[i]
		}
		interceptGradient := 0.0
		for _, sample := range samples {
			prediction := regressionSigmoid(regressionDot(model.Coefficients, model.Intercept, sample.features))
			label := 0.0
			if sample.liked {
				label = 1
			}
			delta := sample.weight * (prediction - label)
			interceptGradient += delta
			for _, feature := range sample.features {
				gradient[feature.index] += delta * feature.value
			}
		}
		for i := range model.Coefficients {
			model.Coefficients[i] -= regressionLearningRate * gradient[i]
		}
		model.Intercept -= regressionLearningRate * interceptGradient
	}
	return model
}

// Predict returns a bounded ranking estimate. Untrained models deliberately
// return 0.5; this value is not a promise of probability calibration.
func (model RegressionModel) Predict(tags, sources []string) float64 {
	if !model.Trained {
		return 0.5
	}
	intercept := model.Intercept
	if !finite(intercept) {
		intercept = 0
	}
	var score float64
	for _, feature := range regressionFeatures(tags, sources) {
		if feature.index >= len(model.Coefficients) {
			continue
		}
		coefficient := model.Coefficients[feature.index]
		if !finite(coefficient) {
			continue
		}
		score += coefficient * feature.value
	}
	score += intercept
	if math.IsNaN(score) {
		return 0.5
	}
	return regressionSigmoid(score)
}

func regressionFeatures(tags, sources []string) []regressionFeature {
	indices := make(map[int]struct{}, len(tags)+len(sources))
	for _, source := range normalizedSources(sources) {
		if index, ok := regressionSourceIndex(source); ok {
			indices[index] = struct{}{}
		}
	}
	for _, tag := range normalizedUniqueTags(tags) {
		index := regressionSourceCount + int(stableTagHash(tag)%uint32(regressionFeatureCount-regressionSourceCount))
		indices[index] = struct{}{}
	}
	if len(indices) == 0 {
		return nil
	}
	ordered := make([]int, 0, len(indices))
	for index := range indices {
		ordered = append(ordered, index)
	}
	sort.Ints(ordered)
	value := 1 / math.Sqrt(float64(len(ordered)))
	features := make([]regressionFeature, 0, len(ordered))
	for _, index := range ordered {
		features = append(features, regressionFeature{index: index, value: value})
	}
	return features
}

func regressionSourceIndex(source string) (int, bool) {
	switch source {
	case SourceRelatedArtist:
		return 0, true
	case SourceRelatedArtwork:
		return 1, true
	case SourceRecommendationFeed:
		return 2, true
	case SourceTagSearch:
		return 3, true
	default:
		return 0, false
	}
}

func stableTagHash(tag string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(tag); i++ {
		h ^= uint32(tag[i])
		h *= 16777619
	}
	return h
}

func regressionSampleLess(a, b regressionSample) bool {
	if a.liked != b.liked {
		return !a.liked
	}
	if a.weight != b.weight {
		return a.weight < b.weight
	}
	limit := len(a.features)
	if len(b.features) < limit {
		limit = len(b.features)
	}
	for i := 0; i < limit; i++ {
		if a.features[i].index != b.features[i].index {
			return a.features[i].index < b.features[i].index
		}
	}
	return len(a.features) < len(b.features)
}

func regressionDot(coefficients []float64, intercept float64, features []regressionFeature) float64 {
	score := intercept
	for _, feature := range features {
		coefficient := coefficients[feature.index]
		score += coefficient * feature.value
	}
	return score
}

func regressionSigmoid(value float64) float64 {
	if math.IsNaN(value) {
		return 0.5
	}
	if value >= 0 {
		exp := math.Exp(-value)
		return 1 / (1 + exp)
	}
	exp := math.Exp(value)
	return exp / (1 + exp)
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
