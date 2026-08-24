package main

import (
	"encoding/json"
	"testing"
)

func TestRealWorldDatasetContract(t *testing.T) {
	items, err := loadQuestions("../../eval/real-world-v1/real-world-v1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 60 {
		t.Fatalf("question count = %d, want 60", len(items))
	}
	formats := map[string]int{"pdf": 0, "docx": 0, "markdown": 0, "txt": 0}
	labels := map[string]int{}
	for _, item := range items {
		if item.Unanswerable && (len(item.ExpectedSources) != 0 || len(item.ExpectedEvidence) != 0) {
			t.Fatalf("unanswerable question %s has expected evidence", item.ID)
		}
		for _, tag := range item.Tags {
			labels[tag]++
			if _, ok := formats[tag]; ok {
				formats[tag]++
			}
		}
	}
	for format, count := range formats {
		if count == 0 {
			t.Errorf("format %s has no questions", format)
		}
	}
	for _, label := range []string{"direct_fact", "semantic_paraphrase", "cross_section", "cross_document", "table_numeric", "similar_term_distractor", "version_conflict", "contextual_followup", "unanswerable"} {
		if labels[label] == 0 {
			t.Errorf("required label %s has no questions", label)
		}
	}
}

func TestReportUsesStableSnakeCaseFields(t *testing.T) {
	payload, err := json.Marshal(report{
		EvaluationType: "real-world-document-evaluation",
		Providers:      providerInfo{DeepSeekReal: true, EmbeddingReal: true, RerankerReal: true},
		Experiments: []strategy{{Cases: []caseResult{{
			ID: "RW-001", AnswerTokens: 1, JudgeTokens: 2, TotalTokens: 3,
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"evaluation_type", "providers", "experiments"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("missing report key %q in %s", key, payload)
		}
	}
	providers := decoded["providers"].(map[string]any)
	for _, key := range []string{"deepseek_real", "embedding_real", "reranker_real"} {
		if providers[key] != true {
			t.Errorf("provider key %q is not true", key)
		}
	}
	cases := decoded["experiments"].([]any)[0].(map[string]any)["cases"].([]any)
	item := cases[0].(map[string]any)
	if item["answer_tokens"] != float64(1) || item["judge_tokens"] != float64(2) || item["total_tokens"] != float64(3) {
		t.Fatalf("token fields were not serialized independently: %#v", item)
	}
}

func TestCalculateMetricsSeparatesAnswerableAndRefusal(t *testing.T) {
	questions := []question{{ID: "a"}, {ID: "u", Unanswerable: true}}
	cases := []caseResult{
		{ID: "a", FirstRelevantRank: 1, CitationHit: true, Judge: judgeResult{AnswerCorrect: true, EvidenceSupported: true}},
		{ID: "u", Judge: judgeResult{CorrectRefusal: true}},
	}
	got := calculateMetrics(cases, questions)
	if got.RecallAt1 != 1 || got.MRR != 1 || got.CitationHitRate != 1 || got.AnswerAccuracy != 1 || got.EvidenceSupportRate != 1 || got.CorrectRefusalRate != 1 || got.HallucinationRate != 0 {
		t.Fatalf("unexpected metrics: %+v", got)
	}
}
