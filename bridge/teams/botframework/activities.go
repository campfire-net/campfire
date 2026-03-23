package botframework

import (
	"encoding/json"
	"fmt"
	"time"
)

// ActivityType represents the type of a Bot Framework activity.
type ActivityType string

const (
	// ActivityTypeMessage is a standard text or card message.
	ActivityTypeMessage ActivityType = "message"

	// ActivityTypeInvoke is an Adaptive Card action invoke.
	ActivityTypeInvoke ActivityType = "invoke"
)

// ChannelAccount identifies a user or bot in a channel.
type ChannelAccount struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// ConversationAccount identifies the conversation.
type ConversationAccount struct {
	ID       string `json:"id"`
	TenantID string `json:"tenantId,omitempty"`
	IsGroup  bool   `json:"isGroup,omitempty"`
	Name     string `json:"name,omitempty"`
}

// Attachment holds a card or file attachment.
type Attachment struct {
	ContentType string          `json:"contentType"`
	Content     json.RawMessage `json:"content,omitempty"`
	ContentURL  string          `json:"contentUrl,omitempty"`
	Name        string          `json:"name,omitempty"`
}

// Activity is a Bot Framework v3 activity (message, invoke, etc.).
type Activity struct {
	Type         ActivityType        `json:"type"`
	ID           string              `json:"id,omitempty"`
	Timestamp    time.Time           `json:"timestamp,omitempty"`
	ServiceURL   string              `json:"serviceUrl,omitempty"`
	ChannelID    string              `json:"channelId,omitempty"`
	From         ChannelAccount      `json:"from,omitempty"`
	Conversation ConversationAccount `json:"conversation,omitempty"`
	Recipient    ChannelAccount      `json:"recipient,omitempty"`
	Text         string              `json:"text,omitempty"`
	ReplyToID    string              `json:"replyToId,omitempty"`
	Attachments  []Attachment        `json:"attachments,omitempty"`
	Importance   string              `json:"importance,omitempty"`
	// Value holds the payload for invoke activities (Adaptive Card action data).
	Value json.RawMessage `json:"value,omitempty"`
}

// ParseActivity decodes an activity from JSON bytes.
// Returns an error if the JSON is malformed or the type field is empty.
func ParseActivity(data []byte) (*Activity, error) {
	var a Activity
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("parsing activity: %w", err)
	}
	if a.Type == "" {
		return nil, fmt.Errorf("activity missing type field")
	}
	return &a, nil
}

// ResourceResponse is returned by Bot Framework on successful activity creation.
type ResourceResponse struct {
	ID string `json:"id"`
}

// ConversationParameters is the request body for CreateConversation.
type ConversationParameters struct {
	IsGroup          bool                `json:"isGroup"`
	Bot              ChannelAccount      `json:"bot"`
	Members          []ChannelAccount    `json:"members,omitempty"`
	TopicName        string              `json:"topicName,omitempty"`
	TenantID         string              `json:"tenantId,omitempty"`
	Activity         *Activity           `json:"activity,omitempty"`
	ChannelData      json.RawMessage     `json:"channelData,omitempty"`
}

// ConversationResourceResponse is returned by CreateConversation.
type ConversationResourceResponse struct {
	ActivityID     string `json:"activityId"`
	ServiceURL     string `json:"serviceUrl"`
	ID             string `json:"id"`
}
