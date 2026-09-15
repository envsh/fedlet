package bilibili

// My favorites (x/v3/fav/folder/created/list-all + x/v3/fav/resource/list).
// Needs SESSDATA. Publishes newly-added favorites only (dedupe by media_id+id).
//
// The folder list is fetched for the logged-in account (DedeUserID), which is
// the only way to see private folders. Each folder is then walked page by page
// (newest-favorite-first, order=mtime) and the favourite records are compared
// against the dedupe set of already-published media ids.

import (
	"fmt"
	"log"
	"strconv"
	"time"
)

const (
	favFoldersURL  = apiHost + "/x/v3/fav/folder/created/list-all?up_mid=%s"
	favResourceURL = apiHost + "/x/v3/fav/resource/list?platform=web&order=mtime"
	// favMaxPages bounds one folder walk (20/page -> 200 newest favourites).
	favMaxPages = 10
)

// favFolder is the per-folder shape of list-all.data.list.
type favFolder struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	MediaCount int64  `json:"media_count"`
}

// favMedia is the per-entry shape of resource/list.data.medias.
type favMedia struct {
	ID      int64  `json:"id"`
	Type    int    `json:"type"`
	Bvid    string `json:"bvid"`
	Title   string `json:"title"`
	Cover   string `json:"cover"`
	FavTime int64  `json:"fav_time"`
	Upper   struct {
		Mid  int64  `json:"mid"`
		Name string `json:"name"`
	} `json:"upper"`
}

type favFoldersData struct {
	List []favFolder `json:"list"`
}

type favResourcesData struct {
	Medias  []favMedia `json:"medias"`
	HasMore bool       `json:"has_more"`
	Count   int64      `json:"count"`
}

// favEntry pairs a favourite media with the folder it was collected from.
type favEntry struct {
	FolderID int64
	Media    favMedia
}

// favMediaID returns the dedupe key for one favourite entry (per-folder, so the
// same video colonising several folders is not double-counted as one event).
func favMediaID(fid int64, it *favMedia) string {
	return fmt.Sprintf("%d:%d", fid, it.ID)
}

// fetchFavFolders lists the folder index of the logged-in account.
func fetchFavFolders() ([]favFolder, error) {
	ensureWarmup()
	mid := jar.get("DedeUserID")
	if jar.get("SESSDATA") == "" || mid == "" {
		return nil, ErrNotLoggedIn
	}
	var data favFoldersData
	if err := getJSON(&data, fmt.Sprintf(favFoldersURL, mid), true); err != nil {
		return nil, err
	}
	return data.List, nil
}

// fetchFavMedia walks one folder's media list, newest-favourite-first, bounded
// by favMaxPages.
func fetchFavMedia(fid int64) ([]favMedia, error) {
	var out []favMedia
	for pn := 1; pn <= favMaxPages; pn++ {
		u := fmt.Sprintf("%s&media_id=%d&pn=%d&ps=20", favResourceURL, fid, pn)
		var data favResourcesData
		if err := getJSON(&data, u, true); err != nil {
			return nil, err
		}
		out = append(out, data.Medias...)
		if !data.HasMore || len(data.Medias) == 0 {
			break
		}
	}
	return out, nil
}

// FetchMyFavorites fetches the recently-added favourites across every folder of
// the logged-in account.
func FetchMyFavorites() ([]favEntry, error) {
	folders, err := fetchFavFolders()
	if err != nil {
		return nil, err
	}
	var out []favEntry
	for i := range folders {
		m, err := fetchFavMedia(folders[i].ID)
		if err != nil {
			return nil, err
		}
		for j := range m {
			out = append(out, favEntry{FolderID: folders[i].ID, Media: m[j]})
		}
	}
	return out, nil
}

// favPath returns a deep link for a favourite entry.
func favPath(it *favMedia) string {
	if it.Bvid != "" {
		return "https://www.bilibili.com/video/" + it.Bvid
	}
	if it.ID != 0 {
		return "https://www.bilibili.com/video/av" + strconv.FormatInt(it.ID, 10)
	}
	return ""
}

// favRound fetches + publishes newly-added favourites. Returns the number
// published (0 on first sync: the existing favourites are seeded silently).
func favRound(state *biliState) int {
	items, err := FetchMyFavorites()
	if err != nil {
		log.Printf("bilibili: favorites error: %v", err)
		pushError(err)
		return 0
	}
	now := time.Now()
	if len(state.Favorites) == 0 {
		for i := range items {
			e := &items[i]
			state.Favorites[favMediaID(e.FolderID, &e.Media)] = now.Unix()
		}
		log.Printf("bilibili: favorites first sync, seeded %d existing items", len(items))
		return 0
	}
	published := 0
	for i := range items {
		e := &items[i]
		it := &e.Media
		key := favMediaID(e.FolderID, it)
		if _, seen := state.Favorites[key]; seen {
			continue
		}
		state.Favorites[key] = now.Unix()
		log.Printf("bilibili: favorite %d %s", it.ID, truncate(it.Title, 60))
		payload := map[string]any{
			"kind":         "bilibili_fav",
			"id":           it.ID,
			"type":         it.Type,
			"title":        it.Title,
			"bvid":         it.Bvid,
			"cover":        it.Cover,
			"fav_time":     it.FavTime,
			"upper":        it.Upper.Name,
			"upper_mid":    it.Upper.Mid,
			"url":          favPath(it),
			"followed_by":  jar.get("DedeUserID"),
			"published_at": now.Unix(),
		}
		if err := publish(payload); err != nil {
			log.Printf("bilibili: publish favorite %d error: %v", it.ID, err)
		}
		published++
	}
	if published > 0 {
		log.Printf("bilibili: favorites round published %d new items", published)
	} else {
		log.Printf("bilibili: favorites round no change")
	}
	return published
}
