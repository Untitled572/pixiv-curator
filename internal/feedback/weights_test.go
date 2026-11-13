package feedback

import (
	"math"
	"testing"
)

func TestLogBiasCalibrationAndTagDeduplication(t *testing.T) {
	tags := []string{" Blue ", "blue"}
	for _, tc := range []struct {
		n    int
		want float64
	}{{0, 0}, {1, .004}, {49, .196}, {50, .2}} {
		got := LogBias(map[string]float64{"blue": float64(tc.n)}, tags)
		if math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("n=%d bias=%g want=%g", tc.n, got, tc.want)
		}
	}
	positive := LogBias(map[string]float64{"blue": 50}, []string{"blue"})
	negative := LogBias(map[string]float64{"blue": -50}, []string{"blue"})
	if positive != .2 || negative != -.2 {
		t.Fatalf("biases are not symmetric: %g %g", positive, negative)
	}
	if got := LogBias(map[string]float64{"blue": 50}, []string{"blue", "unmatched"}); got != .1 {
		t.Fatalf("unmatched tag did not contribute zero to the mean: %g", got)
	}
	if got := LogBias(map[string]float64{"blue": 1000}, []string{"blue"}); got != .4 {
		t.Fatalf("positive cap=%g", got)
	}
	if math.Abs(Weight(map[string]float64{"blue": 50}, []string{"blue"})-math.Exp(.2)) > 1e-12 {
		t.Fatal("weight is not exp(log bias)")
	}
	prob := 1 / (1 + math.Exp(-positive))
	if math.Abs((prob-.5)-.0498339973) > 1e-8 {
		t.Fatalf("50-count probability change=%g", prob-.5)
	}
	one := 1 / (1 + math.Exp(-.004))
	if math.Abs((one-.5)-.0009999973) > 1e-8 {
		t.Fatalf("single-count probability change=%g", one-.5)
	}
}
