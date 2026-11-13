package sqlite

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"
)

type recommendationMixTestRow struct {
	id         int64
	score      float64
	reasons    string
	samples    string
	sources    string
	components map[string]any
}

func addRecommendationMixTestRun(t *testing.T, s *Store, id, budget, stats string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO recommendation_runs(id,state,input_version,budget_json,stats_json,started_at) VALUES(?,'running','v1',?,?,?)`, id, budget, stats, now); err != nil {
		t.Fatal(err)
	}
}

func addRecommendationMixCandidate(t *testing.T, s *Store, run string, artistID, illustID int64, rating string, score float64, sources []string, components string, tags ...string) {
	t.Helper()
	seedArtist(t, s, artistID)
	if _, err := s.db.Exec(`UPDATE artists SET review_status='pending' WHERE id=?`, artistID); err != nil {
		t.Fatal(err)
	}
	seedSelectionArtwork(t, s, illustID, artistID, rating, "non_ai", tags, 1)
	encodedSources, err := json.Marshal(sources)
	if err != nil {
		t.Fatal(err)
	}
	if components == "" {
		components = `{}`
	}
	now := time.Now().UnixMilli()
	if _, err = s.db.Exec(`INSERT INTO recommendations(run_id,artist_id,score,reasons_json,sample_illust_ids_json,sources_json,confidence,score_version,components_json,created_at,updated_at) VALUES(?,?,?,'["untouched"]',?,?, 'high','v1',?,?,?)`, run, artistID, score, mustJSONStrings(t, illustID), string(encodedSources), components, now, now); err != nil {
		t.Fatal(err)
	}
}

func readRecommendationMixRows(t *testing.T, s *Store, run string) []recommendationMixTestRow {
	t.Helper()
	rows, err := s.db.Query(`SELECT artist_id,score,reasons_json,sample_illust_ids_json,sources_json,components_json FROM recommendations WHERE run_id=?`, run)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []recommendationMixTestRow{}
	for rows.Next() {
		var row recommendationMixTestRow
		var components string
		if err = rows.Scan(&row.id, &row.score, &row.reasons, &row.samples, &row.sources, &components); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal([]byte(components), &row.components); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].components["display_order"].(float64) < out[j].components["display_order"].(float64)
	})
	return out
}

func completeRecommendationMixForTest(t *testing.T, s *Store, run string, budget []byte) {
	t.Helper()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = selectIndependentRecommendations(context.Background(), tx, run, budget); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentMixPrioritizesUniformThenRankingAndFreezesSpreadOrder(t *testing.T) {
	s := openTestStore(t)
	const run = "mix-uniform"
	budget := `{"candidate_artists":6,"algorithm_artists":4,"ranking_artists":1,"exploration_artists":2,"uniform_sample_target":1,"library_adult_ratio":0.5,"independent_exploration":true}`
	addRecommendationMixTestRun(t, s, run, budget, `{}`)
	for i := int64(0); i < 6; i++ {
		addRecommendationMixCandidate(t, s, run, 3000+i, 4000+i, "safe", 0.95-float64(i)/100, []string{"related"}, "")
	}
	for i := int64(0); i < 3; i++ {
		addRecommendationMixCandidate(t, s, run, 3100+i, 4100+i, "unknown", 0.01, []string{independentRankingSource}, "")
	}
	for i := int64(0); i < 2; i++ {
		sources := []string{independentUniformSource}
		if i == 0 {
			sources = append(sources, independentRankingSource)
		}
		addRecommendationMixCandidate(t, s, run, 3200+i, 4200+i, "safe", 0.01, sources, "")
	}
	completeRecommendationMixForTest(t, s, run, []byte(budget))
	first := readRecommendationMixRows(t, s, run)
	if len(first) != 6 {
		t.Fatalf("selected %d rows, want 6", len(first))
	}
	independent, normal := 0, 0
	independentPositions := []int{}
	var lastNormalScore float64 = 2
	for pos, row := range first {
		flags := row.components
		score := flags["display_score"].(float64)
		if score < 0 || score > 1 {
			t.Fatalf("display score out of range: %v", score)
		}
		if flags["exploration"] == true {
			independent++
			independentPositions = append(independentPositions, pos)
			var sources []string
			_ = json.Unmarshal([]byte(row.sources), &sources)
			if !containsString(sources, independentUniformSource) && !containsString(sources, independentRankingSource) {
				t.Fatalf("non-independent source marked exploration: %v", sources)
			}
		} else {
			normal++
			if row.score > lastNormalScore {
				t.Fatalf("ordinary order is not descending: %v then %v", lastNormalScore, row.score)
			}
			lastNormalScore = row.score
		}
		if int(flags["display_order"].(float64)) != pos {
			t.Fatalf("display order at pos %d: %v", pos, flags["display_order"])
		}
	}
	if independent != 2 || normal != 4 {
		t.Fatalf("independent=%d normal=%d", independent, normal)
	}
	var actualRaw string
	if err := s.db.QueryRow(`SELECT json_extract(budget_json,'$.actual_source_counts') FROM recommendation_runs WHERE id=?`, run).Scan(&actualRaw); err != nil {
		t.Fatal(err)
	}
	var actual map[string]int
	if err := json.Unmarshal([]byte(actualRaw), &actual); err != nil || actual["algorithm"] != 4 || actual[independentRankingSource] != 1 || actual[independentUniformSource] != 1 {
		t.Fatalf("actual source counts=%v err=%v", actual, err)
	}
	if len(independentPositions) != 2 {
		t.Fatalf("independent positions=%v", independentPositions)
	}
	second := readRecommendationMixRows(t, s, run)
	if len(second) != len(first) {
		t.Fatal("reading frozen mix changed row count")
	}
	for i := range first {
		if first[i].id != second[i].id || first[i].components["display_order"] != second[i].components["display_order"] {
			t.Fatal("persisted display order changed on read")
		}
	}
}

func TestIndependentMixUsesExactAvailable17To10To3CohortBudget(t *testing.T) {
	s := openTestStore(t)
	const run = "mix-17-10-3"
	budget := `{"candidate_artists":30,"algorithm_artists":17,"ranking_artists":10,"exploration_artists":13,"uniform_sample_target":3,"library_adult_ratio":0.5,"independent_exploration":true}`
	addRecommendationMixTestRun(t, s, run, budget, `{}`)
	for i := int64(0); i < 20; i++ {
		addRecommendationMixCandidate(t, s, run, 3800+i, 4800+i, "safe", 0.5, []string{"algorithm"}, "")
	}
	for i := int64(0); i < 20; i++ {
		addRecommendationMixCandidate(t, s, run, 3900+i, 4900+i, "unknown", 0.1, []string{independentRankingSource}, "")
	}
	for i := int64(0); i < 6; i++ {
		addRecommendationMixCandidate(t, s, run, 4000+i, 5000+i, "unknown", 0.1, []string{independentUniformSource}, "")
	}
	completeRecommendationMixForTest(t, s, run, []byte(budget))
	rows := readRecommendationMixRows(t, s, run)
	if len(rows) != 30 {
		t.Fatalf("selected=%d, want 30", len(rows))
	}
	var algorithm, ranking, uniform int
	for _, row := range rows {
		var sources []string
		if err := json.Unmarshal([]byte(row.sources), &sources); err != nil {
			t.Fatal(err)
		}
		switch {
		case containsString(sources, independentUniformSource):
			uniform++
		case containsString(sources, independentRankingSource):
			ranking++
		default:
			algorithm++
		}
	}
	if algorithm != 17 || ranking != 10 || uniform != 3 {
		t.Fatalf("actual cohorts algorithm=%d ranking=%d uniform=%d", algorithm, ranking, uniform)
	}
	var actualRaw string
	if err := s.db.QueryRow(`SELECT json_extract(budget_json,'$.actual_source_counts') FROM recommendation_runs WHERE id=?`, run).Scan(&actualRaw); err != nil {
		t.Fatal(err)
	}
	var actual map[string]int
	if err := json.Unmarshal([]byte(actualRaw), &actual); err != nil || actual["algorithm"] != 17 || actual[independentRankingSource] != 10 || actual[independentUniformSource] != 3 {
		t.Fatalf("actual source counts=%v err=%v", actual, err)
	}
}

func TestIndependentMixStratifiesAdultSafeUnknownAndPreservesCarryover(t *testing.T) {
	s := openTestStore(t)
	const run = "mix-strata"
	budget := `{"candidate_artists":10,"algorithm_artists":10,"ranking_artists":0,"exploration_artists":0,"uniform_sample_target":0,"library_adult_ratio":0.5,"independent_exploration":true}`
	addRecommendationMixTestRun(t, s, run, budget, `{}`)
	for i := int64(0); i < 12; i++ {
		rating := "safe"
		if i < 6 {
			rating = "r18"
		}
		if i == 11 {
			rating = "unknown"
		}
		addRecommendationMixCandidate(t, s, run, 3300+i, 4300+i, rating, 0.5, []string{"related"}, "")
	}
	// This carried row is blocked and has invalid samples, but its immutable
	// recommendation payload and previous exploration bit must survive.
	seedArtist(t, s, 3399)
	if _, err := s.db.Exec(`UPDATE artists SET review_status='blocked' WHERE id=3399`); err != nil {
		t.Fatal(err)
	}
	seedArtist(t, s, 3398)
	if _, err := s.db.Exec(`UPDATE artists SET review_status='blocked' WHERE id=3398`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO recommendations(run_id,artist_id,score,reasons_json,sample_illust_ids_json,sources_json,confidence,score_version,components_json,created_at,updated_at) VALUES(?,3398,.61,'["older carry"]','[999998]','["prior"]','high','v1','{"carryover":true,"display_order":2}',?,?)`, run, now, now); err != nil {
		t.Fatal(err)
	}
	carryComponents := `{"carryover":true,"exploration":true,"display_order":4,"display_score":0.2,"keep":"verbatim"}`
	if _, err := s.db.Exec(`INSERT INTO recommendations(run_id,artist_id,score,reasons_json,sample_illust_ids_json,sources_json,confidence,score_version,components_json,created_at,updated_at) VALUES(?,3399,.77,'["carry"]','[999999]','["prior"]','high','v1',?,?,?)`, run, carryComponents, now, now); err != nil {
		t.Fatal(err)
	}
	completeRecommendationMixForTest(t, s, run, []byte(budget))
	rows := readRecommendationMixRows(t, s, run)
	var adult, safe, unknown, carry int
	carryPositions := map[int64]int{}
	for pos, row := range rows {
		switch row.id {
		case 3398:
			carry++
			carryPositions[row.id] = pos
			if row.score != 0.61 || row.reasons != `["older carry"]` || row.samples != `[999998]` || row.sources != `["prior"]` {
				t.Fatalf("older carryover changed: %+v", row)
			}
		case 3399:
			carry++
			carryPositions[row.id] = pos
			if row.score != 0.77 || row.reasons != `["carry"]` || row.samples != `[999999]` || row.sources != `["prior"]` || row.components["exploration"] != true || row.components["display_score"] != 0.2 || row.components["keep"] != "verbatim" {
				t.Fatalf("carryover changed: %+v", row)
			}
		case 3300, 3301, 3302, 3303, 3304, 3305:
			adult++
		case 3306, 3307, 3308, 3309, 3310:
			safe++
		case 3311:
			unknown++
		}
	}
	if len(rows) != 12 || carry != 2 || carryPositions[3398] >= carryPositions[3399] || adult != 5 || safe != 4 || unknown != 1 {
		t.Fatalf("len=%d carry=%d adult=%d safe=%d unknown=%d", len(rows), carry, adult, safe, unknown)
	}
}

func TestIndependentMixFeedbackBiasAdjustsRatioConservativelyAndRejectsInvalidSamples(t *testing.T) {
	s := openTestStore(t)
	const run = "mix-feedback"
	budget := `{"candidate_artists":30,"algorithm_artists":30,"ranking_artists":0,"exploration_artists":0,"uniform_sample_target":0,"library_adult_ratio":0.5,"independent_exploration":true}`
	stats := `{"feedback_tag_counts":{"adult-tag":10000,"safe-tag":-10000}}`
	addRecommendationMixTestRun(t, s, run, budget, stats)
	for i := int64(0); i < 40; i++ {
		rating, tag := "safe", "safe-tag"
		if i < 20 {
			rating, tag = "r18g", "adult-tag"
		}
		addRecommendationMixCandidate(t, s, run, 3400+i, 4400+i, rating, 0.5, []string{"related"}, "", tag)
	}
	seedArtist(t, s, 3499)
	if _, err := s.db.Exec(`UPDATE artists SET review_status='blocked' WHERE id=3499`); err != nil {
		t.Fatal(err)
	}
	addRecommendationMixCandidate(t, s, run, 3500, 4500, "r18", 1, []string{"related"}, "")
	if _, err := s.db.Exec(`UPDATE image_pages SET media_state='unavailable' WHERE illust_id=4500 AND page_index=0`); err != nil {
		t.Fatal(err)
	}
	completeRecommendationMixForTest(t, s, run, []byte(budget))
	rows := readRecommendationMixRows(t, s, run)
	var adult, safe, blockedOrInvalid int
	for _, row := range rows {
		if row.id >= 3400 && row.id < 3420 {
			adult++
		} else if row.id >= 3420 && row.id < 3440 {
			safe++
		} else {
			blockedOrInvalid++
		}
	}
	if len(rows) != 30 || adult < 17 || adult > 19 || safe < 11 || safe > 13 || blockedOrInvalid != 0 {
		t.Fatalf("selected=%d adult=%d safe=%d blocked/invalid=%d", len(rows), adult, safe, blockedOrInvalid)
	}
	// Unknown must remain outside the safe stratum: with a zero adult target,
	// an available safe candidate wins its dedicated slot over unknown.
	const unknownRun = "mix-unknown"
	unknownBudget := `{"candidate_artists":1,"algorithm_artists":1,"ranking_artists":0,"exploration_artists":0,"uniform_sample_target":0,"library_adult_ratio":0,"independent_exploration":true}`
	addRecommendationMixTestRun(t, s, unknownRun, unknownBudget, `{}`)
	addRecommendationMixCandidate(t, s, unknownRun, 3600, 4600, "unknown", 1, []string{"related"}, "")
	addRecommendationMixCandidate(t, s, unknownRun, 3601, 4601, "safe", 0, []string{"related"}, "")
	completeRecommendationMixForTest(t, s, unknownRun, []byte(unknownBudget))
	unknownRows := readRecommendationMixRows(t, s, unknownRun)
	if len(unknownRows) != 1 || unknownRows[0].id != 3601 {
		t.Fatalf("unknown treated as safe: %+v", unknownRows)
	}
}

func TestIndependentMixDoesNotMarkOrdinaryAsRandomAndDoesNotOverfill(t *testing.T) {
	s := openTestStore(t)
	const run = "mix-short"
	budget := `{"candidate_artists":5,"algorithm_artists":2,"ranking_artists":2,"exploration_artists":3,"uniform_sample_target":1,"library_adult_ratio":0.5,"independent_exploration":true}`
	addRecommendationMixTestRun(t, s, run, budget, `{}`)
	addRecommendationMixCandidate(t, s, run, 3700, 4700, "safe", 0.5, []string{"related"}, "")
	addRecommendationMixCandidate(t, s, run, 3701, 4701, "safe", 0.4, []string{"related"}, "")
	addRecommendationMixCandidate(t, s, run, 3702, 4702, "safe", 0.3, []string{independentUniformSource}, "")
	addRecommendationMixCandidate(t, s, run, 3703, 4703, "safe", 0.2, []string{independentRankingSource}, "")
	addRecommendationMixCandidate(t, s, run, 3705, 4705, "safe", 0.25, []string{independentRankingSource}, "")
	if _, err := s.db.Exec(`UPDATE artists SET review_status='blocked' WHERE id=3705`); err != nil {
		t.Fatal(err)
	}
	addRecommendationMixCandidate(t, s, run, 3706, 4706, "safe", 0.5, []string{independentRankingSource}, "")
	if _, err := s.db.Exec(`UPDATE image_pages SET media_state='unavailable' WHERE illust_id=4706 AND page_index=0`); err != nil {
		t.Fatal(err)
	}
	// A page-level explicit dislike removes the only remaining otherwise-valid independent option.
	addRecommendationMixCandidate(t, s, run, 3704, 4704, "safe", 1, []string{independentRankingSource}, "")
	if _, err := s.db.Exec(`INSERT INTO image_feedback(illust_id,page_index,value,updated_at) VALUES(4704,0,'dislike',1)`); err != nil {
		t.Fatal(err)
	}
	completeRecommendationMixForTest(t, s, run, []byte(budget))
	rows := readRecommendationMixRows(t, s, run)
	if len(rows) != 4 {
		t.Fatalf("selected=%d, want available 4", len(rows))
	}
	for _, row := range rows {
		isIndependent := row.components["exploration"] == true
		var sources []string
		_ = json.Unmarshal([]byte(row.sources), &sources)
		if isIndependent != (containsString(sources, independentRankingSource) || containsString(sources, independentUniformSource)) {
			t.Fatalf("source marker mismatch: %+v", row)
		}
		if row.id == 3704 {
			t.Fatalf("feedback-disliked candidate %d selected", row.id)
		}
		if row.id == 3705 || row.id == 3706 {
			t.Fatalf("blocked or invalid candidate selected: %d", row.id)
		}
	}
}
