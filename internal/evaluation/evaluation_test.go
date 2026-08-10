package evaluation

import (
	"os"
	"strings"
	"testing"
)

func TestM5DatasetHasSixtyValidQuestions(t *testing.T) {
	file, err := os.Open("../../eval/datasets/knowflow-m5.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	items, err := LoadDataset(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 60 {
		t.Fatalf("questions=%d", len(items))
	}
}

func TestLoadDatasetRequiresFiftyQuestions(t *testing.T) {
	_, err := LoadDataset(strings.NewReader(`{"id":"q-1","knowledge_base":"demo","question":"q","expected_chunk_ids":["c"],"reference_answer":"a"}` + "\n"))
	if err == nil || !strings.Contains(err.Error(), "at least 50") {
		t.Fatalf("error=%v", err)
	}
}

func TestCalculateMetrics(t *testing.T) {
	m := calculate([]CaseResult{{FirstRelevantRank: 1, CitationHit: true, RetrievalLatencyMS: 1, EndToEndLatencyMS: 2, Tokens: 10}, {FirstRelevantRank: 6, RetrievalLatencyMS: 3, EndToEndLatencyMS: 4, Tokens: 20}})
	if m.RecallAt1 != .5 || m.RecallAt5 != .5 || m.RecallAt10 != 1 || m.MRR != (1+1.0/6)/2 || m.CitationHitRate != .5 || m.P95RetrievalLatencyMS != 3 {
		t.Fatalf("metrics=%#v", m)
	}
}
