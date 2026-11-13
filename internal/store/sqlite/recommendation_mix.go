package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"

	"github.com/Untitled572/pixiv-curator/internal/domain"
	"github.com/Untitled572/pixiv-curator/internal/feedback"
)

const (
	independentRankingSource = "independent_ranking"
	independentUniformSource = "independent_uniform_id"
)

type recommendationMixBudget struct {
	CandidateArtists    int     `json:"candidate_artists"`
	ExplorationArtists  int     `json:"exploration_artists"`
	AlgorithmArtists    int     `json:"algorithm_artists"`
	RankingArtists      int     `json:"ranking_artists"`
	UniformSampleTarget int     `json:"uniform_sample_target"`
	LibraryAdultRatio   float64 `json:"library_adult_ratio"`
	Independent         bool    `json:"independent_exploration"`
}

type recommendationMixCandidate struct {
	artistID   int64
	score      float64
	samples    []int64
	components string
	sources    []string
	tags       []string
	class      string
	weight     float64
}

type recommendationMixCarry struct {
	artistID     int64
	score        float64
	components   string
	exploration  bool
	displayScore float64
}

// selectIndependentRecommendations freezes the independent and ordinary
// cohorts into one persisted order. It operates only on the completion tx.
func selectIndependentRecommendations(ctx context.Context, tx *sql.Tx, runID string, raw []byte) error {
	var b recommendationMixBudget
	if err := json.Unmarshal(raw, &b); err != nil || !b.Independent || b.CandidateArtists < 0 || b.CandidateArtists > 60 || b.AlgorithmArtists < 0 || b.RankingArtists < 0 || b.ExplorationArtists < 0 || b.ExplorationArtists > b.CandidateArtists || b.UniformSampleTarget < 0 || b.UniformSampleTarget > b.ExplorationArtists || b.AlgorithmArtists+b.RankingArtists+b.UniformSampleTarget != b.CandidateArtists || b.RankingArtists+b.UniformSampleTarget != b.ExplorationArtists || math.IsNaN(b.LibraryAdultRatio) || b.LibraryAdultRatio < 0 || b.LibraryAdultRatio > 1 {
		return domain.ErrInvalid
	}
	var statsRaw string
	if err := tx.QueryRowContext(ctx, `SELECT stats_json FROM recommendation_runs WHERE id=?`, runID).Scan(&statsRaw); err != nil {
		return err
	}
	var stats struct {
		FeedbackTagCounts map[string]float64 `json:"feedback_tag_counts"`
	}
	if len(statsRaw) > 0 {
		if err := json.Unmarshal([]byte(statsRaw), &stats); err != nil {
			return domain.ErrInvalid
		}
	}

	carry, err := readRecommendationMixCarry(ctx, tx, runID)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.artist_id,r.score,r.sample_illust_ids_json,r.components_json,r.sources_json
		FROM recommendations r JOIN artists a ON a.id=r.artist_id
		WHERE r.run_id=? AND a.review_status='pending' AND COALESCE(json_extract(r.components_json,'$.carryover'),0)<>1
		ORDER BY r.artist_id`, runID)
	if err != nil {
		return err
	}
	type candidateRaw struct {
		candidate  recommendationMixCandidate
		samplesRaw string
		sourcesRaw string
	}
	rawCandidates := make([]candidateRaw, 0)
	for rows.Next() {
		var rc candidateRaw
		if err = rows.Scan(&rc.candidate.artistID, &rc.candidate.score, &rc.samplesRaw, &rc.candidate.components, &rc.sourcesRaw); err != nil {
			rows.Close()
			return err
		}
		rawCandidates = append(rawCandidates, rc)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	candidates := make([]recommendationMixCandidate, 0, len(rawCandidates))
	for _, rc := range rawCandidates {
		c := rc.candidate
		if err = json.Unmarshal([]byte(rc.sourcesRaw), &c.sources); err != nil {
			return domain.ErrInvalid
		}
		var sampleOK bool
		c.samples, sampleOK = decodeRecommendationMixSampleIDs(rc.samplesRaw)
		if !sampleOK || len(c.samples) == 0 || len(c.samples) > 100 {
			continue
		}
		profiles, valid, profileErr := readRecommendationMixSamples(ctx, tx, c.artistID, c.samples)
		if profileErr != nil {
			return profileErr
		}
		if !valid {
			continue
		}
		c.class, c.tags = classifyRecommendationMixSamples(profiles)
		c.weight = math.Exp(4*c.score) * feedback.Weight(stats.FeedbackTagCounts, c.tags)
		candidates = append(candidates, c)
	}

	independent := make([]recommendationMixCandidate, 0)
	normal := make([]recommendationMixCandidate, 0)
	uniform := make([]recommendationMixCandidate, 0)
	for _, c := range candidates {
		hasRanking, hasUniform := hasRecommendationMixSource(c.sources, independentRankingSource), hasRecommendationMixSource(c.sources, independentUniformSource)
		if hasRanking || hasUniform {
			independent = append(independent, c)
			if hasUniform {
				uniform = append(uniform, c)
			}
		} else {
			normal = append(normal, c)
		}
	}

	targetIndependent := min(b.ExplorationArtists, b.CandidateArtists)
	selectedIndependent := make([]recommendationMixCandidate, 0, targetIndependent)
	selectedIDs := make(map[int64]bool, b.CandidateArtists)
	actualCounts := map[string]int{"algorithm": 0, independentRankingSource: 0, independentUniformSource: 0}
	// Uniform-ID samples get first claim on exploration slots. A row carrying
	// both independent sources is one candidate and is counted only once.
	uniformPick := chooseRecommendationMixUniform(uniform, min(b.UniformSampleTarget, targetIndependent))
	for _, c := range uniformPick {
		selectedIndependent = append(selectedIndependent, c)
		selectedIDs[c.artistID] = true
		actualCounts[independentUniformSource]++
	}
	rankingPool := make([]recommendationMixCandidate, 0, len(independent))
	for _, c := range independent {
		if !selectedIDs[c.artistID] && hasRecommendationMixSource(c.sources, independentRankingSource) {
			rankingPool = append(rankingPool, c)
		}
	}
	rankingPick := chooseRecommendationMixUniform(rankingPool, b.RankingArtists)
	selectedIndependent = append(selectedIndependent, rankingPick...)
	for _, c := range rankingPick {
		selectedIDs[c.artistID] = true
		actualCounts[independentRankingSource]++
	}
	selectedNormal := selectRecommendationMixNormal(normal, b.AlgorithmArtists, b.LibraryAdultRatio, stats.FeedbackTagCounts)
	for _, c := range selectedNormal {
		selectedIDs[c.artistID] = true
		actualCounts["algorithm"]++
	}
	fillSlots := b.CandidateArtists - len(selectedIndependent) - len(selectedNormal)
	if fillSlots > 0 {
		remainingNormal := filterRecommendationMixCandidates(normal, selectedIDs)
		moreNormal := selectRecommendationMixNormal(remainingNormal, fillSlots, b.LibraryAdultRatio, stats.FeedbackTagCounts)
		selectedNormal = append(selectedNormal, moreNormal...)
		for _, c := range moreNormal {
			selectedIDs[c.artistID] = true
			actualCounts["algorithm"]++
		}
	}
	fillSlots = b.CandidateArtists - len(selectedIndependent) - len(selectedNormal)
	if fillSlots > 0 {
		remainingRanking := filterRecommendationMixCandidates(rankingPool, selectedIDs)
		moreRanking := chooseRecommendationMixUniform(remainingRanking, fillSlots)
		selectedIndependent = append(selectedIndependent, moreRanking...)
		for _, c := range moreRanking {
			selectedIDs[c.artistID] = true
			actualCounts[independentRankingSource]++
		}
	}
	fillSlots = b.CandidateArtists - len(selectedIndependent) - len(selectedNormal)
	if fillSlots > 0 {
		remainingIndependent := filterRecommendationMixCandidates(independent, selectedIDs)
		moreIndependent := chooseRecommendationMixUniform(remainingIndependent, fillSlots)
		selectedIndependent = append(selectedIndependent, moreIndependent...)
		for _, c := range moreIndependent {
			selectedIDs[c.artistID] = true
			if hasRecommendationMixSource(c.sources, independentUniformSource) {
				actualCounts[independentUniformSource]++
			} else {
				actualCounts[independentRankingSource]++
			}
		}
	}

	// Carryovers retain their previous relative order and all stored fields.
	// New ordinary results stay score-ranked; independent results are inserted
	// uniformly among the available display positions.
	sort.SliceStable(selectedNormal, func(i, j int) bool {
		if selectedNormal[i].score == selectedNormal[j].score {
			return selectedNormal[i].artistID < selectedNormal[j].artistID
		}
		return selectedNormal[i].score > selectedNormal[j].score
	})
	ordered := make([]recommendationMixEntry, 0, len(carry)+len(selectedNormal)+len(selectedIndependent))
	for _, c := range carry {
		ordered = append(ordered, recommendationMixEntry{artistID: c.artistID, score: c.score, components: c.components, carryover: true, exploration: c.exploration, displayScore: c.displayScore})
	}
	for _, c := range selectedNormal {
		ordered = append(ordered, recommendationMixEntry{artistID: c.artistID, score: c.score, components: c.components})
	}
	rand.Shuffle(len(selectedIndependent), func(i, j int) {
		selectedIndependent[i], selectedIndependent[j] = selectedIndependent[j], selectedIndependent[i]
	})
	for _, c := range selectedIndependent {
		at := rand.IntN(len(ordered) + 1)
		ordered = append(ordered, recommendationMixEntry{})
		copy(ordered[at+1:], ordered[at:])
		ordered[at] = recommendationMixEntry{artistID: c.artistID, score: c.score, components: c.components, exploration: true}
	}
	setRecommendationMixDisplayScores(ordered)
	for i := range ordered {
		component, marshalErr := mergeRecommendationMixComponents(ordered[i].components, i, ordered[i].score, ordered[i].exploration, ordered[i].carryover, ordered[i].displayScore)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE recommendations SET components_json=? WHERE run_id=? AND artist_id=?`, component, runID, ordered[i].artistID); err != nil {
			return err
		}
	}
	actualJSON, err := json.Marshal(actualCounts)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE recommendation_runs SET budget_json=json_set(budget_json,'$.actual_source_counts',json(?)) WHERE id=?`, string(actualJSON), runID); err != nil {
		return err
	}

	if len(ordered) == 0 {
		_, err = tx.ExecContext(ctx, `DELETE FROM recommendations WHERE run_id=? AND COALESCE(json_extract(components_json,'$.carryover'),0)<>1`, runID)
		return err
	}
	args := []any{runID}
	for _, item := range ordered {
		args = append(args, item.artistID)
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM recommendations WHERE run_id=? AND COALESCE(json_extract(components_json,'$.carryover'),0)<>1 AND artist_id NOT IN (`+placeholders(len(ordered))+`)`, args...)
	return err
}

func decodeRecommendationMixSampleIDs(raw string) ([]int64, bool) {
	var values []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
		return nil, false
	}
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		var id int64
		if len(value) > 0 && value[0] == '"' {
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				return nil, false
			}
			parsed, err := strconv.ParseInt(text, 10, 64)
			if err != nil || strconv.FormatInt(parsed, 10) != text {
				return nil, false
			}
			id = parsed
		} else {
			parsed, err := strconv.ParseInt(string(value), 10, 64)
			if err != nil {
				return nil, false
			}
			id = parsed
		}
		ids = append(ids, id)
	}
	return ids, true
}

type recommendationMixSample struct {
	rating string
	tags   []string
}

func readRecommendationMixSamples(ctx context.Context, tx *sql.Tx, artistID int64, ids []int64) ([]recommendationMixSample, bool, error) {
	if len(ids) == 0 {
		return nil, false, nil
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return nil, false, nil
		}
		seen[id] = true
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT w.id,w.rating,t.normalized_name
		FROM json_each(?) j
		JOIN artworks w ON w.id=CAST(j.value AS INTEGER) AND w.artist_id=? AND w.visible=1
		JOIN image_pages p ON p.illust_id=w.id AND p.page_index=0 AND p.media_state='indexed'
		LEFT JOIN artwork_preferences ap ON ap.illust_id=w.id AND ap.value='dislike'
		LEFT JOIN image_feedback f ON f.illust_id=w.id AND f.page_index=0 AND f.value='dislike'
		LEFT JOIN artwork_tags at ON at.illust_id=w.id
		LEFT JOIN tags t ON t.id=at.tag_id
		WHERE j.type='integer' AND ap.illust_id IS NULL AND f.illust_id IS NULL ORDER BY w.id,t.normalized_name`, string(encoded), artistID)
	if err != nil {
		return nil, false, err
	}
	byID := make(map[int64]*recommendationMixSample, len(ids))
	tagSet := make(map[string]bool)
	for rows.Next() {
		var id int64
		var rating string
		var tag sql.NullString
		if err = rows.Scan(&id, &rating, &tag); err != nil {
			rows.Close()
			return nil, false, err
		}
		profile := byID[id]
		if profile == nil {
			profile = &recommendationMixSample{rating: rating}
			byID[id] = profile
		}
		if tag.Valid && tag.String != "" && !containsString(profile.tags, tag.String) {
			profile.tags = append(profile.tags, tag.String)
		}
		if tag.Valid && tag.String != "" {
			tagSet[tag.String] = true
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, false, err
	}
	if err = rows.Close(); err != nil {
		return nil, false, err
	}
	if len(byID) != len(ids) {
		return nil, false, nil
	}
	out := make([]recommendationMixSample, 0, len(ids))
	tagList := make([]string, 0, len(tagSet))
	for _, id := range ids {
		out = append(out, *byID[id])
	}
	for tag := range tagSet {
		tagList = append(tagList, tag)
	}
	// Attach the same deduplicated profile tags to each sample; classifier only
	// inspects ratings, while the weighting helper deduplicates tags itself.
	for i := range out {
		out[i].tags = tagList
	}
	return out, true, nil
}

func classifyRecommendationMixSamples(samples []recommendationMixSample) (string, []string) {
	if len(samples) == 0 {
		return "unknown", nil
	}
	allSafe := true
	tagSet := make(map[string]bool)
	hasAdult := false
	for _, sample := range samples {
		if sample.rating == "r18" || sample.rating == "r18g" {
			hasAdult = true
		}
		if sample.rating != "safe" {
			allSafe = false
		}
		for _, tag := range sample.tags {
			tagSet[tag] = true
		}
	}
	tags := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	if hasAdult {
		return "adult", tags
	}
	if !allSafe {
		return "unknown", tags
	}
	return "safe", tags
}

type recommendationMixEntry struct {
	artistID     int64
	score        float64
	components   string
	exploration  bool
	carryover    bool
	displayScore float64
}

func selectRecommendationMixNormal(candidates []recommendationMixCandidate, slots int, libraryRatio float64, counts map[string]float64) []recommendationMixCandidate {
	if slots <= 0 || len(candidates) == 0 {
		return nil
	}
	adult, safe, unknown := make([]recommendationMixCandidate, 0), make([]recommendationMixCandidate, 0), make([]recommendationMixCandidate, 0)
	var adultBias, safeBias float64
	for _, c := range candidates {
		switch c.class {
		case "adult":
			adult = append(adult, c)
			adultBias += feedback.LogBias(counts, c.tags)
		case "safe":
			safe = append(safe, c)
			safeBias += feedback.LogBias(counts, c.tags)
		default:
			unknown = append(unknown, c)
		}
	}
	ratio := libraryRatio
	if ratio > 0 && ratio < 1 && len(adult) > 0 && len(safe) > 0 {
		delta := 0.5 * (adultBias/float64(len(adult)) - safeBias/float64(len(safe)))
		logit := math.Log(ratio / (1 - ratio))
		ratio = 1 / (1 + math.Exp(-(logit + delta)))
	}
	unknownSlots := min(len(unknown), int(math.Round(float64(slots)*0.10)))
	classSlots := slots - unknownSlots
	adultSlots := min(len(adult), int(math.Round(float64(classSlots)*ratio)))
	safeSlots := min(len(safe), classSlots-adultSlots)
	selected := make([]recommendationMixCandidate, 0, min(slots, len(candidates)))
	chosen := make(map[int64]bool)
	take := func(pool []recommendationMixCandidate, n int) []recommendationMixCandidate {
		got := chooseRecommendationMixWeighted(pool, n)
		for _, c := range got {
			chosen[c.artistID] = true
			selected = append(selected, c)
		}
		return got
	}
	pickedAdult := take(adult, adultSlots)
	pickedSafe := take(safe, safeSlots)
	_ = pickedAdult
	_ = pickedSafe
	take(unknown, unknownSlots)
	if len(selected) < slots {
		remaining := filterRecommendationMixCandidates(adult, chosen)
		more := chooseRecommendationMixWeighted(remaining, slots-len(selected))
		for _, c := range more {
			chosen[c.artistID] = true
			selected = append(selected, c)
		}
	}
	if len(selected) < slots {
		remaining := filterRecommendationMixCandidates(safe, chosen)
		more := chooseRecommendationMixWeighted(remaining, slots-len(selected))
		for _, c := range more {
			chosen[c.artistID] = true
			selected = append(selected, c)
		}
	}
	if len(selected) < slots {
		remaining := filterRecommendationMixCandidates(unknown, chosen)
		more := chooseRecommendationMixWeighted(remaining, slots-len(selected))
		for _, c := range more {
			chosen[c.artistID] = true
			selected = append(selected, c)
		}
	}
	if len(selected) < slots {
		more := chooseRecommendationMixWeighted(filterRecommendationMixCandidates(candidates, chosen), slots-len(selected))
		selected = append(selected, more...)
	}
	return selected
}

func chooseRecommendationMixUniform(candidates []recommendationMixCandidate, count int) []recommendationMixCandidate {
	if count <= 0 || len(candidates) == 0 {
		return nil
	}
	items := append([]recommendationMixCandidate(nil), candidates...)
	rand.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	return items[:min(count, len(items))]
}

func chooseRecommendationMixWeighted(candidates []recommendationMixCandidate, count int) []recommendationMixCandidate {
	if count <= 0 || len(candidates) == 0 {
		return nil
	}
	items := append([]recommendationMixCandidate(nil), candidates...)
	selected := make([]recommendationMixCandidate, 0, min(count, len(items)))
	for len(selected) < count && len(items) > 0 {
		var total float64
		for _, c := range items {
			total += c.weight
		}
		if total <= 0 || math.IsInf(total, 0) || math.IsNaN(total) {
			rand.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
			selected = append(selected, items[:min(count-len(selected), len(items))]...)
			break
		}
		pick := rand.Float64() * total
		index := len(items) - 1
		for i, c := range items {
			pick -= c.weight
			if pick < 0 {
				index = i
				break
			}
		}
		selected = append(selected, items[index])
		items = append(items[:index], items[index+1:]...)
	}
	return selected
}

func filterRecommendationMixCandidates(candidates []recommendationMixCandidate, selected map[int64]bool) []recommendationMixCandidate {
	out := make([]recommendationMixCandidate, 0, len(candidates))
	for _, c := range candidates {
		if !selected[c.artistID] {
			out = append(out, c)
		}
	}
	return out
}

func readRecommendationMixCarry(ctx context.Context, tx *sql.Tx, runID string) ([]recommendationMixCarry, error) {
	rows, err := tx.QueryContext(ctx, `SELECT artist_id,score,components_json FROM recommendations WHERE run_id=? AND COALESCE(json_extract(components_json,'$.carryover'),0)=1 ORDER BY CASE WHEN json_type(components_json,'$.display_order') IN ('integer','real') THEN 0 ELSE 1 END,CAST(json_extract(components_json,'$.display_order') AS INTEGER),score DESC,artist_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []recommendationMixCarry{}
	for rows.Next() {
		var c recommendationMixCarry
		if err = rows.Scan(&c.artistID, &c.score, &c.components); err != nil {
			return nil, err
		}
		var previous struct {
			Exploration  bool     `json:"exploration"`
			DisplayScore *float64 `json:"display_score"`
		}
		if err = json.Unmarshal([]byte(c.components), &previous); err != nil {
			return nil, err
		}
		c.exploration = previous.Exploration
		c.displayScore = clampRecommendationMixScore(c.score)
		if previous.DisplayScore != nil {
			c.displayScore = clampRecommendationMixScore(*previous.DisplayScore)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func mergeRecommendationMixComponents(raw string, order int, score float64, exploration, carryover bool, displayScore float64) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return "", err
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	orderJSON, _ := json.Marshal(order)
	fields["display_order"] = orderJSON
	if carryover {
		return stringMustJSON(fields)
	}
	fields["exploration"], _ = json.Marshal(exploration)
	if exploration {
		fields["display_score"], _ = json.Marshal(clampRecommendationMixScore(displayScore))
	} else {
		fields["display_score"], _ = json.Marshal(clampRecommendationMixScore(score))
	}
	return stringMustJSON(fields)
}

func stringMustJSON(v any) (string, error) { b, err := json.Marshal(v); return string(b), err }

func setRecommendationMixDisplayScores(items []recommendationMixEntry) {
	known := make([]int, 0, len(items))
	for i := range items {
		if items[i].carryover && items[i].exploration {
			items[i].displayScore = clampRecommendationMixScore(items[i].displayScore)
			known = append(known, i)
		} else if !items[i].exploration {
			items[i].displayScore = clampRecommendationMixScore(items[i].score)
			known = append(known, i)
		}
	}
	if len(known) == 0 {
		for i := range items {
			if items[i].exploration && !items[i].carryover {
				items[i].displayScore = 0.5
			}
		}
		return
	}
	for i := range items {
		if !items[i].exploration || items[i].carryover {
			continue
		}
		left, right := -1, -1
		for _, k := range known {
			if k < i {
				left = k
			}
			if k > i {
				right = k
				break
			}
		}
		switch {
		case left >= 0 && right >= 0:
			fraction := float64(i-left) / float64(right-left)
			items[i].displayScore = items[left].displayScore + fraction*(items[right].displayScore-items[left].displayScore)
		case left >= 0:
			items[i].displayScore = items[left].displayScore
		case right >= 0:
			items[i].displayScore = items[right].displayScore
		default:
			items[i].displayScore = 0.5
		}
		items[i].displayScore = clampRecommendationMixScore(items[i].displayScore)
	}
}

func clampRecommendationMixScore(score float64) float64 { return math.Max(0, math.Min(1, score)) }

func hasRecommendationMixSource(sources []string, want string) bool {
	return containsString(sources, want)
}

func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
