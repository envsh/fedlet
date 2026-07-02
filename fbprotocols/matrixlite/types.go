package matrixlite

type Message struct {
	EventID   string `json:"event_id,omitempty"`
	Sender    string `json:"sender,omitempty"`
	Body      string `json:"body,omitempty"`
	MsgType   string `json:"msgtype,omitempty"`
	RoomID    string `json:"room_id,omitempty"`
	Timestamp int64  `json:"origin_server_ts,omitempty"`
}

type Config struct {
	Server   string `json:"server"`
	User     string `json:"user"`
	Password string `json:"password"`
}
