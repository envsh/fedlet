package fbshared

// Protocol 常量
const (
	ProtoEmailImap    = "emailimap"
	ProtoOutlookGraph = "outlookgraph"
	ProtoToxOverHttp  = "toxoverhttp"
	ProtoMisskey      = "misskey"
	ProtoGomuks       = "gomuks"
	ProtoIRCCloud     = "irccloud"
	ProtoMatrixLite   = "matrixlite"
	ProtoIRCLounge    = "irclounge"
)

// MsgFormat 常量
const (
	FmtText     = "text"
	FmtMarkdown = "markdown"
	FmtHTML     = "html"
)

// AttachType 常量
const (
	AttachImage   = "image"
	AttachVideo   = "video"
	AttachAudio   = "audio"
	AttachFile    = "file"
	AttachSticker = "sticker"
	AttachGif     = "gif"
)

// MsgType 常量
const (
	MsgTypeCreate         = "create"
	MsgTypeEdit           = "edit"
	MsgTypeDelete         = "delete"
	MsgTypeReactionAdd    = "reaction_add"
	MsgTypeReactionRemove = "reaction_remove"
	MsgTypeJoin           = "join"
	MsgTypeLeave          = "leave"
	MsgTypeFileUpload     = "file_upload"
)

type UnifiedMessage struct {
	Text      string `json:"text,omitempty"`
	Markdown  string `json:"markdown,omitempty"`
	HTML      string `json:"html,omitempty"`
	MsgFormat string `json:"msgformat,omitempty"`

	Protocol    string `json:"protocol"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	ChatID      string `json:"chat_id,omitempty"`
	ChatName    string `json:"chat_name,omitempty"`
	Gateway     string `json:"gateway,omitempty"`

	Username string `json:"username,omitempty"`
	UserID   string `json:"userid,omitempty"`   // sometimes internal id
	Usernick string `json:"usernick,omitempty"` // display name/nick
	UsrIcon  string `json:"usr_icon,omitempty"`
	// UsrPresence and UsrStatus come from m.presence: the online/offline state
	// and the user's own status_msg. Both are best-effort, since a homeserver
	// may have presence disabled, so they are frequently absent.
	UsrPresence string `json:"usr_presence,omitempty"`
	UsrStatus   string `json:"usr_status,omitempty"`
	ChanIcon    string `json:"chan_icon,omitempty"`

	ChatTopic string `json:"chat_topic,omitempty"`
	ChatAlias string `json:"chat_alias,omitempty"`
	// ChatType is the room type, e.g. m.space.
	ChatType string `json:"chat_type,omitempty"`
	// ChatMembers is the joined member count.
	ChatMembers int `json:"chat_members,omitempty"`
	// ChatFederated reports whether the room federates across homeservers
	// (m.room.create m.federate). Omitted when unknown.
	ChatFederated *bool `json:"chat_federated,omitempty"`

	MsgType   string   `json:"msgtype,omitempty"`
	MsgID     string   `json:"msgid,omitempty"`
	ReplyTos  []string `json:"reply_tos,omitempty"`
	Mentions  []string `json:"mentions,omitempty"`
	Timestamp int64    `json:"timestamp"`

	Attachments []Attachment `json:"attachments,omitempty"`
	Raw         []byte       `json:"-"`
}

// RoomProfileWire is the published form of a chat's metadata. Field names
// follow the MSC3266 RoomSummary response, the one stable Matrix spec that
// describes a room's attribute set; Federated is our own extension, since it
// comes from m.room.create and is not returned by /summary.
type RoomProfileWire struct {
	Name             string `json:"name,omitempty"`
	AvatarURL        string `json:"avatar_url,omitempty"`
	Topic            string `json:"topic,omitempty"`
	Alias            string `json:"canonical_alias,omitempty"`
	NumJoinedMembers int    `json:"num_joined_members,omitempty"`
	RoomType         string `json:"room_type,omitempty"`
	RoomVersion      string `json:"room_version,omitempty"`
	Federated        *bool  `json:"federated,omitempty"`
}

// ApplyRoomProfile copies a room profile onto the message. Empty fields are
// left untouched so a profile that only knows the topic does not erase an
// already-resolved channel name.
func (um *UnifiedMessage) ApplyRoomProfile(w RoomProfileWire) {
	if um == nil {
		return
	}
	if w.Name != "" {
		um.ChatName = w.Name
	}
	if w.AvatarURL != "" {
		um.ChanIcon = w.AvatarURL
	}
	if w.Topic != "" {
		um.ChatTopic = w.Topic
	}
	if w.Alias != "" {
		um.ChatAlias = w.Alias
	}
	if w.RoomType != "" {
		um.ChatType = w.RoomType
	}
	if w.NumJoinedMembers > 0 {
		um.ChatMembers = w.NumJoinedMembers
	}
	if w.Federated != nil {
		um.ChatFederated = w.Federated
	}
}

// MemberProfileWire is the published form of one user's attributes. Field names
// follow m.room.member, plus the presence status, which is the only text a
// Matrix user attaches to themselves.
type MemberProfileWire struct {
	UserID      string `json:"user_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	Presence    string `json:"presence,omitempty"`
	StatusMsg   string `json:"status_msg,omitempty"`
}

// ApplySenderProfile copies member attributes onto the message. Empty fields are
// left untouched so a sparse profile cannot erase what another path already
// resolved.
func (um *UnifiedMessage) ApplySenderProfile(w MemberProfileWire) {
	if um == nil {
		return
	}
	if w.DisplayName != "" {
		um.Usernick = w.DisplayName
	}
	if w.AvatarURL != "" {
		um.UsrIcon = w.AvatarURL
	}
	if w.Presence != "" {
		um.UsrPresence = w.Presence
	}
	if w.StatusMsg != "" {
		um.UsrStatus = w.StatusMsg
	}
}

type Attachment struct {
	MimeType   string `json:"mimetype"`
	Size       int    `json:"size,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	Filename   string `json:"filename,omitempty"`
	AttachType string `json:"attachtype,omitempty"`
	URL        string `json:"url,omitempty"`
}
