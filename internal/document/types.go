package document

import "time"

const (
	StatusUploaded  = "uploaded"
	StatusQueued    = "queued"
	StatusParsing   = "parsing"
	StatusChunking  = "chunking"
	StatusEmbedding = "embedding"
	StatusReady     = "ready"
	StatusFailed    = "failed"
	StatusDeleting  = "deleting"
)

type Document struct {
	ID              string        `json:"id"`
	KnowledgeBaseID string        `json:"knowledge_base_id"`
	Filename        string        `json:"filename"`
	MIMEType        string        `json:"mime_type"`
	SizeBytes       int64         `json:"size_bytes"`
	SHA256          string        `json:"sha256"`
	Status          string        `json:"status"`
	ChunkCount      int           `json:"chunk_count"`
	IndexVersion    int           `json:"index_version"`
	ErrorCode       *string       `json:"error_code,omitempty"`
	ErrorMessage    *string       `json:"error_message,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
	Job             *IngestionJob `json:"job,omitempty"`
}

type IngestionJob struct {
	ID           string     `json:"id"`
	DocumentID   string     `json:"document_id"`
	IndexVersion int        `json:"index_version"`
	Status       string     `json:"status"`
	Stage        string     `json:"stage"`
	Progress     int        `json:"progress"`
	Attempts     int        `json:"attempts"`
	ErrorCode    *string    `json:"error_code,omitempty"`
	ErrorMessage *string    `json:"error_message,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

type Chunk struct {
	ID          string         `json:"id"`
	ChunkIndex  int            `json:"chunk_index"`
	Content     string         `json:"content"`
	TokenCount  int            `json:"token_count"`
	PageStart   *int           `json:"page_start,omitempty"`
	PageEnd     *int           `json:"page_end,omitempty"`
	HeadingPath *string        `json:"heading_path,omitempty"`
	Metadata    map[string]any `json:"metadata"`
}

type Page struct {
	Items    []Document `json:"items"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
	Total    int64      `json:"total"`
}

type ChunkPage struct {
	Items    []Chunk `json:"items"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
	Total    int64   `json:"total"`
}
