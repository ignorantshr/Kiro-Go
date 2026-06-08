package proxy

// Wire types for the OpenAI Responses API (/v1/responses). These mirror the
// public OpenAI schema closely enough that standard clients work unchanged;
// the proxy converts between these and the internal Kiro payload.

import "encoding/json"

// ResponsesRequest is the inbound /v1/responses request body. Input is kept as
// raw JSON because it may be a plain string, a single item object, or an array
// of typed input items (see parseResponsesInput).
type ResponsesRequest struct {
	Model              string            `json:"model"`
	Input              json.RawMessage   `json:"input"`
	Instructions       string            `json:"instructions,omitempty"`
	Stream             bool              `json:"stream,omitempty"`
	Tools              []OpenAITool      `json:"tools,omitempty"`
	ToolChoice         json.RawMessage   `json:"tool_choice,omitempty"`
	PreviousResponseID string            `json:"previous_response_id,omitempty"`
	Store              *bool             `json:"store,omitempty"`
	Temperature        *float64          `json:"temperature,omitempty"`
	MaxOutputTokens    *int              `json:"max_output_tokens,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

// ResponsesObject is the response returned to the client. The Stored* fields
// (json:"-") are not serialized to the client but are persisted to disk so a
// later request can resume via PreviousResponseID.
type ResponsesObject struct {
	ID                 string               `json:"id"`
	Object             string               `json:"object"`
	CreatedAt          int64                `json:"created_at"`
	Status             string               `json:"status"`
	Model              string               `json:"model"`
	Output             []ResponseOutputItem `json:"output"`
	Usage              ResponsesUsage       `json:"usage"`
	PreviousResponseID string               `json:"previous_response_id,omitempty"`
	Metadata           map[string]string    `json:"metadata,omitempty"`
	Error              *ResponsesError      `json:"error,omitempty"`
	Instructions       string               `json:"instructions,omitempty"`
	StoredInput        json.RawMessage      `json:"-"`
	StoredInstr        string               `json:"-"`
	StoredAt           int64                `json:"stored_at,omitempty"`
}

// ResponseOutputItem is one item in a response's Output array: an assistant
// message, a tool/function call, or similar. Tool-call items carry CallID,
// Name and Arguments; message items carry Role and Content.
type ResponseOutputItem struct {
	ID        string                `json:"id"`
	Type      string                `json:"type"`
	Role      string                `json:"role,omitempty"`
	Status    string                `json:"status,omitempty"`
	Content   []ResponseContentPart `json:"content,omitempty"`
	CallID    string                `json:"call_id,omitempty"`
	Name      string                `json:"name,omitempty"`
	Arguments string                `json:"arguments,omitempty"`
}

// ResponseContentPart is a single content fragment within an output message.
type ResponseContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ResponsesUsage reports token consumption for a response.
type ResponsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// ResponsesError describes a failed response in the OpenAI error shape.
type ResponsesError struct {
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}
