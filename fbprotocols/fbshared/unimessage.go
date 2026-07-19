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
	Text      string   `json:"text,omitempty"`
	Markdown  string   `json:"markdown,omitempty"`
	HTML      string   `json:"html,omitempty"`
	MsgFormat string   `json:"msgformat,omitempty"`

	Protocol string `json:"protocol"`
	Account  string `json:"account,omitempty"`
	ChatID   string `json:"chat_id,omitempty"`
	ChatName string `json:"chat_name,omitempty"`
	Gateway  string `json:"gateway,omitempty"`

	Username string `json:"username,omitempty"`
	UserID   string `json:"userid,omitempty"`
	Avatar   string `json:"avatar,omitempty"`

	MsgType  string   `json:"msgtype,omitempty"`
	MsgID    string   `json:"msgid,omitempty"`
	ReplyTos []string `json:"reply_tos,omitempty"`
	Mentions []string `json:"mentions,omitempty"`
	Timestamp int64   `json:"timestamp"`

	Attachments []Attachment `json:"attachments,omitempty"`
	Raw         []byte       `json:"-"`
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
