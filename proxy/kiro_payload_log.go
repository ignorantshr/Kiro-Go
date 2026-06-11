package proxy

import (
	"encoding/json"
	"fmt"
)

type kiroPayloadLogView struct {
	ConversationState kiroConversationStateLogView `json:"conversationState"`
	ProfileArn        string                       `json:"profileArn,omitempty"`
	InferenceConfig   *InferenceConfig             `json:"inferenceConfig,omitempty"`
}

type kiroConversationStateLogView struct {
	AgentContinuationId string                    `json:"agentContinuationId,omitempty"`
	AgentTaskType       string                    `json:"agentTaskType,omitempty"`
	ChatTriggerType     string                    `json:"chatTriggerType"`
	ConversationID      string                    `json:"conversationId"`
	CurrentMessage      kiroCurrentMessageLogView `json:"currentMessage"`
	History             []kiroHistoryLogView      `json:"history,omitempty"`
}

type kiroCurrentMessageLogView struct {
	UserInputMessage kiroUserInputMessageLogView `json:"userInputMessage"`
}

type kiroUserInputMessageLogView struct {
	Content                 string                          `json:"content"`
	ModelID                 string                          `json:"modelId,omitempty"`
	Origin                  string                          `json:"origin"`
	Images                  []KiroImage                     `json:"images,omitempty"`
	UserInputMessageContext *userInputMessageContextLogView `json:"userInputMessageContext,omitempty"`
}

type userInputMessageContextLogView struct {
	Tools       []KiroToolWrapper       `json:"tools,omitempty"`
	ToolResults []kiroToolResultLogView `json:"toolResults,omitempty"`
}

type kiroToolResultLogView struct {
	ToolUseID string                     `json:"toolUseId"`
	Content   []kiroResultContentLogView `json:"content"`
	Status    string                     `json:"status"`
}

type kiroResultContentLogView struct {
	Text string `json:"text"`
}

type kiroHistoryLogView struct {
	UserInputMessage         *kiroUserInputMessageLogView         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *kiroAssistantResponseMessageLogView `json:"assistantResponseMessage,omitempty"`
}

type kiroAssistantResponseMessageLogView struct {
	Content  string        `json:"content"`
	ToolUses []KiroToolUse `json:"toolUses,omitempty"`
}

func redactContentForLog(content string) string {
	return fmt.Sprintf("*** redacted content len=%d ***", len(content))
}

func makeResultContentLogView(content string) kiroResultContentLogView {
	return kiroResultContentLogView{Text: redactContentForLog(content)}
}

func makeResultContentLogViews(contents []KiroResultContent) []kiroResultContentLogView {
	if len(contents) == 0 {
		return nil
	}
	views := make([]kiroResultContentLogView, len(contents))
	for i, content := range contents {
		views[i] = makeResultContentLogView(content.Text)
	}
	return views
}

func makeToolResultLogViews(results []KiroToolResult) []kiroToolResultLogView {
	if len(results) == 0 {
		return nil
	}
	views := make([]kiroToolResultLogView, len(results))
	for i, result := range results {
		views[i] = kiroToolResultLogView{
			ToolUseID: result.ToolUseID,
			Status:    result.Status,
			Content:   makeResultContentLogViews(result.Content),
		}
	}
	return views
}

func makeUserInputMessageLogView(msg KiroUserInputMessage) kiroUserInputMessageLogView {
	view := kiroUserInputMessageLogView{
		Content: redactContentForLog(msg.Content),
		ModelID: msg.ModelID,
		Origin:  msg.Origin,
		Images:  msg.Images,
	}
	if msg.UserInputMessageContext != nil {
		view.UserInputMessageContext = &userInputMessageContextLogView{
			Tools:       msg.UserInputMessageContext.Tools,
			ToolResults: makeToolResultLogViews(msg.UserInputMessageContext.ToolResults),
		}
	}
	return view
}

func makeHistoryLogViews(history []KiroHistoryMessage) []kiroHistoryLogView {
	if len(history) == 0 {
		return nil
	}
	views := make([]kiroHistoryLogView, len(history))
	for i, msg := range history {
		if msg.UserInputMessage != nil {
			userView := makeUserInputMessageLogView(*msg.UserInputMessage)
			views[i].UserInputMessage = &userView
		}
		if msg.AssistantResponseMessage != nil {
			views[i].AssistantResponseMessage = &kiroAssistantResponseMessageLogView{
				Content:  redactContentForLog(msg.AssistantResponseMessage.Content),
				ToolUses: msg.AssistantResponseMessage.ToolUses,
			}
		}
	}
	return views
}

func makePayloadLogView(payload *KiroPayload) *kiroPayloadLogView {
	if payload == nil {
		return nil
	}
	return &kiroPayloadLogView{
		ConversationState: kiroConversationStateLogView{
			AgentContinuationId: payload.ConversationState.AgentContinuationId,
			AgentTaskType:       payload.ConversationState.AgentTaskType,
			ChatTriggerType:     payload.ConversationState.ChatTriggerType,
			ConversationID:      payload.ConversationState.ConversationID,
			CurrentMessage: kiroCurrentMessageLogView{
				UserInputMessage: makeUserInputMessageLogView(payload.ConversationState.CurrentMessage.UserInputMessage),
			},
			History: makeHistoryLogViews(payload.ConversationState.History),
		},
		ProfileArn:      payload.ProfileArn,
		InferenceConfig: payload.InferenceConfig,
	}
}

func formatPayloadForErrorLog(payload *KiroPayload) string {
	if payload == nil {
		return "<nil>"
	}
	sanitized, err := json.Marshal(makePayloadLogView(payload))
	if err != nil {
		return fmt.Sprintf("<payload log marshal failed: %v>", err)
	}
	return string(sanitized)
}
