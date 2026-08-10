package knowledgebase

import (
	"encoding/json"
	"time"
)

type RetrievalConfig struct {
	ChunkSize     int     `json:"chunk_size"`
	ChunkOverlap  int     `json:"chunk_overlap"`
	DenseTopK     int     `json:"dense_top_k"`
	SparseTopK    int     `json:"sparse_top_k"`
	RerankTopK    int     `json:"rerank_top_k"`
	FinalTopK     int     `json:"final_top_k"`
	MinimumScore  float64 `json:"minimum_score"`
	RRFK          int     `json:"rrf_k"`
	RerankEnabled bool    `json:"rerank_enabled"`
}

func DefaultRetrievalConfig() RetrievalConfig {
	return RetrievalConfig{
		ChunkSize: 800, ChunkOverlap: 120, DenseTopK: 20, SparseTopK: 20,
		RerankTopK: 10, FinalTopK: 5, MinimumScore: 0, RRFK: 60,
	}
}

type KnowledgeBase struct {
	ID                 string          `json:"id"`
	OwnerID            string          `json:"owner_id"`
	Name               string          `json:"name"`
	Description        string          `json:"description"`
	EmbeddingModel     string          `json:"embedding_model"`
	EmbeddingDimension int             `json:"embedding_dimension"`
	RetrievalConfig    RetrievalConfig `json:"retrieval_config"`
	DocumentCount      int64           `json:"document_count"`
	ReadyChunkCount    int64           `json:"ready_chunk_count"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type CreateInput struct {
	Name            string           `json:"name"`
	Description     string           `json:"description"`
	EmbeddingModel  string           `json:"embedding_model"`
	RetrievalConfig *RetrievalConfig `json:"retrieval_config"`
}

type UpdateInput struct {
	Name            *string          `json:"name"`
	Description     *string          `json:"description"`
	RetrievalConfig *RetrievalConfig `json:"retrieval_config"`
	EmbeddingModel  *string          `json:"embedding_model"`
}

type Page struct {
	Items    []KnowledgeBase `json:"items"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
	Total    int64           `json:"total"`
}

func configJSON(config RetrievalConfig) ([]byte, error) { return json.Marshal(config) }
