package zhihu

// Own collection lists (favlists) + their contents. Needs the main z_c0
// session. Publishes newly-collected items only (dedupe by collection+item).
//
// Endpoints (field-verified live 2026-09):
//
//	GET /api/v4/members/{url_token}/favlists?offset=&limit=
//	GET /api/v4/collections/{cid}/contents?offset=&limit=
//
// The account's own url_token comes from /api/v4/me and is cached for the
// process lifetime (invalidated when a round reports ErrNotLoggedIn).

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

const (
	favlistsURL           = "https://www.zhihu.com/api/v4/members/%s/favlists?offset=%d&limit=%d"
	collectionContentsURL = "https://www.zhihu.com/api/v4/collections/%d/contents?offset=%d&limit=%d"

	favlistPageSize = 20
	// collectionPageCap bounds one walk: 10 pages of collections, 10 pages per
	// collection (200 newest-collected items per collection).
	collectionPageCap = 10
)

// ZhihuCollection is one favourite-collection (收藏夹) summary.
type ZhihuCollection struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ItemCount   int    `json:"item_count"`
	IsPublic    bool   `json:"is_public"`
	UpdatedTime int64  `json:"updated_time"`
	CreatedTime int64  `json:"created_time"`
}

// CollectionItem is one entry inside a collection (answer/article/video/pin).
type CollectionItem struct {
	ID          int64  `json:"id"`
	ContentType string `json:"content_type"`
	CreatedTime int64  `json:"created_time"`
	UpdatedTime int64  `json:"updated_time"`
	Title       string `json:"title"`
	Excerpt     string `json:"excerpt"`
	URL         string `json:"url"`
	Author      string `json:"author"`
	QuestionID  int64  `json:"question_id"`
	VoteupCount int64  `json:"voteup_count"`
	Raw         json.RawMessage `json:"-"`
}

// CollectionEntry pairs a collected item with its collection.
type CollectionEntry struct {
	CollectionID    int64
	CollectionTitle string
	Item            CollectionItem
}

// collectionURLToken cache of the account's own url_token (guarded by
// urlTokenMu); empty until the first successful /me fetch.
var (
	urlTokenMu  sync.Mutex
	urlTokenVal string
)

// currentUserURLToken returns the logged-in account's url_token, fetching and
// caching /api/v4/me on first use.
func currentUserURLToken() (string, error) {
	urlTokenMu.Lock()
	defer urlTokenMu.Unlock()
	if urlTokenVal != "" {
		return urlTokenVal, nil
	}
	var me meResp
	if err := getJSON(&me, meURL, true); err != nil {
		return "", err
	}
	if me.UrlToken == "" {
		return "", fmt.Errorf("zhihu: /api/v4/me returned no url_token (user=%s)", me.Name)
	}
	urlTokenVal = me.UrlToken
	return urlTokenVal, nil
}

// clearOwnURLToken drops the cached url_token after a session rejection.
func clearOwnURLToken() {
	urlTokenMu.Lock()
	urlTokenVal = ""
	urlTokenMu.Unlock()
}

// parseCollectionItem decodes one +/+content entry into a flat view.
func parseCollectionItem(raw json.RawMessage) CollectionItem {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return CollectionItem{}
	}
	it := CollectionItem{
		ID:          intField(m, "id"),
		ContentType: strField(m, "type"),
		CreatedTime: intField(m, "created_time"),
		UpdatedTime: intField(m, "updated_time"),
		Title:       strField(m, "title"),
		Excerpt:     strField(m, "excerpt"),
		URL:         strField(m, "url"),
		VoteupCount: intField(m, "voteup_count"),
		Raw:         append(json.RawMessage(nil), raw...),
	}
	if a, ok := m["author"].(map[string]any); ok {
		it.Author = strField(a, "name")
	}
	if it.ContentType == "" {
		if att, ok := m["attachment"].(map[string]any); ok {
			it.ContentType = strings.ToLower(strField(att, "type"))
		}
	}
	if q, ok := m["question"].(map[string]any); ok {
		it.QuestionID = intField(q, "id")
		if it.Title == "" {
			it.Title = strField(q, "title")
		}
	}
	if it.URL == "" && it.ContentType == "answer" && it.QuestionID != 0 && it.ID != 0 {
		it.URL = fmt.Sprintf("https://www.zhihu.com/question/%d/answer/%d", it.QuestionID, it.ID)
	}
	return it
}

// fetchMyFavlists lists the account's own collections (private included).
func fetchMyFavlists() ([]ZhihuCollection, error) {
	tok, err := currentUserURLToken()
	if err != nil {
		return nil, err
	}
	ref := "https://www.zhihu.com/people/" + tok + "/collections"
	var out []ZhihuCollection
	offset := 0
	for page := 0; page < collectionPageCap; page++ {
		u := fmt.Sprintf(favlistsURL, tok, offset, favlistPageSize)
		var r struct {
			Data   []ZhihuCollection `json:"data"`
			Paging struct {
				IsEnd bool `json:"is_end"`
			} `json:"paging"`
		}
		if err := getJSONRef(&r, u, true, ref); err != nil {
			return nil, err
		}
		out = append(out, r.Data...)
		if r.Paging.IsEnd || len(r.Data) == 0 {
			break
		}
		offset += len(r.Data)
	}
	return out, nil
}

// fetchCollectionContents lists one collection's items, newest first.
func fetchCollectionContents(cid int64) ([]CollectionItem, error) {
	ref := fmt.Sprintf("https://www.zhihu.com/collection/%d", cid)
	var out []CollectionItem
	offset := 0
	for page := 0; page < collectionPageCap; page++ {
		u := fmt.Sprintf(collectionContentsURL, cid, offset, favlistPageSize)
		var r struct {
			Data   []json.RawMessage `json:"data"`
			Paging struct {
				IsEnd bool `json:"is_end"`
			} `json:"paging"`
		}
		if err := getJSONRef(&r, u, true, ref); err != nil {
			return nil, err
		}
		for i := range r.Data {
			out = append(out, parseCollectionItem(r.Data[i]))
		}
		if r.Paging.IsEnd || len(r.Data) == 0 {
			break
		}
		offset += len(r.Data)
	}
	return out, nil
}

// FetchMyCollectionItems walks every own collection and returns the recent
// items newest-first.
func FetchMyCollectionItems() ([]CollectionEntry, error) {
	collections, err := fetchMyFavlists()
	if err != nil {
		return nil, err
	}
	var out []CollectionEntry
	for i := range collections {
		col := &collections[i]
		items, err := fetchCollectionContents(col.ID)
		if err != nil {
			return nil, err
		}
		for j := range items {
			out = append(out, CollectionEntry{CollectionID: col.ID, CollectionTitle: col.Title, Item: items[j]})
		}
	}
	return out, nil
}

// collectionItemKey returns the dedupe key (collection+item).
func collectionItemKey(ce *CollectionEntry) string {
	return fmt.Sprintf("%d:%d", ce.CollectionID, ce.Item.ID)
}

// collectionRound fetches + publishes newly-collected items. Returns the
// number published (0 on first sync: existing items are seeded silently).
func collectionRound(state *zhihuState) {
	entries, err := FetchMyCollectionItems()
	if err != nil {
		log.Printf("zhihu: collections error: %v", err)
		handleRoundErr(state, err)
		if errors.Is(err, ErrNotLoggedIn) {
			clearOwnURLToken()
		}
		return
	}
	now := time.Now()
	if len(state.Collections) == 0 {
		for i := range entries {
			state.Collections[collectionItemKey(&entries[i])] = now.Unix()
		}
		log.Printf("zhihu: collections first sync, seeded %d existing items", len(entries))
		return
	}
	published := 0
	for i := range entries {
		ce := &entries[i]
		key := collectionItemKey(ce)
		if _, seen := state.Collections[key]; seen {
			continue
		}
		state.Collections[key] = now.Unix()
		log.Printf("zhihu: collection %s:%d %s", ce.Item.ContentType, ce.Item.ID, truncate(ce.Item.Title, 60))
		payload := map[string]any{
			"kind":             "zhihu_collection",
			"collected_at":     ce.Item.CreatedTime,
			"collection_id":    ce.CollectionID,
			"collection_title": ce.CollectionTitle,
			"content_id":       ce.Item.ID,
			"content_type":     ce.Item.ContentType,
			"title":            ce.Item.Title,
			"excerpt":          ce.Item.Excerpt,
			"author":           ce.Item.Author,
			"question_id":      ce.Item.QuestionID,
			"voteup_count":     ce.Item.VoteupCount,
			"url":              ce.Item.URL,
			"published_at":     now.Unix(),
		}
		raw, aerr := fbshared.InsertFlatFields(ce.Item.Raw, map[string]any{
			"proto_type":  payload["kind"],
			"cycle_count": len(entries),
		})
		if aerr != nil {
			log.Printf("zhihu: publish collection %s error: %v", key, aerr)
		} else if err := publish(raw); err != nil {
			log.Printf("zhihu: publish collection %s error: %v", key, err)
		}
		published++
	}
	if published > 0 {
		log.Printf("zhihu: collections round published %d new items", published)
	} else {
		log.Printf("zhihu: collections round no change")
	}
}
