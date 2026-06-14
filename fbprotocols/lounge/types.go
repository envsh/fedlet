package lounge

type MessageType string

const (
	MsgAction    MessageType = "action"
	MsgMessage   MessageType = "message"
	MsgNotice    MessageType = "notice"
	MsgJoin      MessageType = "join"
	MsgPart      MessageType = "part"
	MsgQuit      MessageType = "quit"
	MsgKick      MessageType = "kick"
	MsgNick      MessageType = "nick"
	MsgTopic     MessageType = "topic"
	MsgTopicSet  MessageType = "topic_set_by"
	MsgMode      MessageType = "mode"
	MsgModeChan  MessageType = "mode_channel"
	MsgModeUser  MessageType = "mode_user"
	MsgInvite    MessageType = "invite"
	MsgError     MessageType = "error"
	MsgRaw       MessageType = "raw"
	MsgWhois     MessageType = "whois"
	MsgCtcp      MessageType = "ctcp"
	MsgChghost   MessageType = "chghost"
	MsgMonospace MessageType = "monospace_block"
	MsgWallops   MessageType = "wallops"
	MsgLogin     MessageType = "login"
	MsgLogout    MessageType = "logout"
	MsgPlugin    MessageType = "plugin"
)

type ChanState int

const (
	ChanParted ChanState = 0
	ChanJoined ChanState = 1
)

type ChanType string

const (
	ChanChannel ChanType = "channel"
	ChanLobby   ChanType = "lobby"
	ChanQuery   ChanType = "query"
	ChanSpecial ChanType = "special"
)

type UserInMessage struct {
	Nick string `json:"nick"`
	Mode string `json:"mode"`
}

type Message struct {
	ID        int            `json:"id"`
	From      *UserInMessage `json:"from,omitempty"`
	Text      string         `json:"text,omitempty"`
	Type      MessageType    `json:"type"`
	Time      string         `json:"time"`
	Self      bool           `json:"self,omitempty"`
	Highlight bool           `json:"highlight,omitempty"`
	Hostmask  string         `json:"hostmask,omitempty"`
	MsgID     string         `json:"msgid,omitempty"`
}

type Channel struct {
	ID       int        `json:"id"`
	Name     string     `json:"name"`
	Topic    string     `json:"topic"`
	Type     ChanType   `json:"type"`
	State    ChanState  `json:"state"`
	Unread   int        `json:"unread"`
	Muted    bool       `json:"muted"`
	Key      string     `json:"key,omitempty"`
	Users    []User     `json:"users,omitempty"`
	Messages []Message  `json:"messages,omitempty"`
}

type Network struct {
	UUID     string         `json:"uuid"`
	Name     string         `json:"name"`
	Nick     string         `json:"nick"`
	Status   NetworkStatus  `json:"status"`
	Channels []Channel      `json:"channels"`
}

type NetworkStatus struct {
	Connected bool `json:"connected"`
	Secure    bool `json:"secure"`
}

type User struct {
	Nick string `json:"nick"`
	Mode string `json:"mode"`
}

type Event struct {
	Type string
	Data []byte
}
