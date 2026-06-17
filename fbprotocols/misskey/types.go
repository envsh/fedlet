package misskey

type Config struct {
	Host     string
	Token    string
	Timeline string
}

type Note struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	UserID      string `json:"userId"`
	User        struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"user"`
	Visibility  string `json:"visibility"`
	CreatedAt   string `json:"createdAt"`
	CW          string `json:"cw"`
	FileIDs     []string `json:"fileIds"`
}

type noteCreateReq struct {
	I          string `json:"i"`
	Text       string `json:"text"`
	Visibility string `json:"visibility"`
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
