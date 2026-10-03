package model

// ChatEvent is the shared Kafka wire format used by WS and IM producers.
// Broker access is internal; user-supplied JSON is not an authorized event.
type ChatEvent struct {
	MsgID       string `json:"msg_id"`
	FromID      int64  `json:"from_id"`
	SenderType  int8   `json:"sender_type"`
	InitiatorID int64  `json:"initiator_id"`
	ToID        int64  `json:"to_id"`
	ChatType    int    `json:"chat_type"`
	ContentType int    `json:"content_type"`
	Content     string `json:"content"`
	Timestamp   int64  `json:"timestamp"`
}
