package bus

type InboundMessage struct {
	Channel    string            `json:"channel"`
	SenderID   string            `json:"sender_id"`
	ChatID     string            `json:"chat_id"`
	Content    string            `json:"content"`
	Media      []string          `json:"media,omitempty"`
	SessionKey string            `json:"session_key"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

type OutboundMessage struct {
	Channel    string `json:"channel"`
	ChatID     string `json:"chat_id"`
	Content    string `json:"content"`
	SessionKey string `json:"session_key,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	IsFinal    bool   `json:"is_final,omitempty"`
	Control    bool   `json:"control,omitempty"` // internal signal message, not user-visible text
}

type MessageHandler func(InboundMessage) error
