package chat

import "time"

type Conversation struct {
	ID              string    `json:"id"`
	UserID          string    `json:"user_id"`
	KnowledgeBaseID string    `json:"knowledge_base_id"`
	Title           string    `json:"title"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Message struct {
	ID               string         `json:"id"`
	ConversationID   string         `json:"conversation_id"`
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	Status           string         `json:"status"`
	Citations        []Citation     `json:"citations"`
	RetrievalTrace   map[string]any `json:"retrieval_trace"`
	Model            *string        `json:"model,omitempty"`
	PromptTokens     int            `json:"prompt_tokens"`
	CompletionTokens int            `json:"completion_tokens"`
	TotalTokens      int            `json:"total_tokens"`
	EstimatedCostUSD float64        `json:"estimated_cost_usd"`
	LatencyMS        int            `json:"latency_ms"`
	ErrorCode        *string        `json:"error_code,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
}

type Citation struct {
	Number      int     `json:"number"`
	DocumentID  string  `json:"document_id"`
	Filename    string  `json:"filename"`
	ChunkID     string  `json:"chunk_id"`
	Excerpt     string  `json:"excerpt"`
	PageStart   *int    `json:"page_start,omitempty"`
	PageEnd     *int    `json:"page_end,omitempty"`
	HeadingPath *string `json:"heading_path,omitempty"`
	ChunkIndex  int     `json:"chunk_index"`
	Location    string  `json:"location"`
	Score       float64 `json:"score"`
}

type Detail struct {
	Conversation Conversation `json:"conversation"`
	Messages     []Message    `json:"messages"`
}

type Page struct {
	Items    []Conversation `json:"items"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Total    int64          `json:"total"`
}

type StreamEvent struct {
	Name string
	Data any
}
