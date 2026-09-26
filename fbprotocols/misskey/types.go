package misskey

type Config struct {
	Host     string
	Token    string
	Timeline string
}

// Note 字段不全,仅为按需声明的子集。
// 拉取到的完整原始结构已通过 raw 事件(map[string]any)原样发布,
// 本结构仅用于构建统一消息(um)与日志,废弃留作大概参考,非权威 schema。
type Note struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	UserID      string `json:"userId"`
	User        struct {
		// User 与 Note 同理:仅 username/name,完整作者信息(avatarUrl/host 等)以 raw 事件为准。
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"user"`
	Visibility  string `json:"visibility"`
	CreatedAt   string `json:"createdAt"`
	CW          string `json:"cw"`
	FileIDs     []string `json:"fileIds"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
}

type noteCreateReq struct {
	I          string   `json:"i"`
	Text       string   `json:"text"`
	Visibility string   `json:"visibility"`
	FileIds    []string `json:"fileIds,omitempty"`
}

type DriveFile struct {
	ID string `json:"id"`
}

type timelineReq struct {
	I       string `json:"i"`
	Limit   int    `json:"limit"`
	SinceID string `json:"sinceId,omitempty"`
}

type iReq struct {
	I string `json:"i"`
}

type iResp struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

type metaResp struct {
	Version string `json:"version"`
	Name    string `json:"name"`
}

type stateData struct {
	SinceID string `json:"sinceId"`
}
