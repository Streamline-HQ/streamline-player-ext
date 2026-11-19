package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ticksPerSecond is Jellyfin's unit of time, 100 ns.
const ticksPerSecond = 10_000_000

// jellyfinItem is a movie, series or episode as /Items lists it for a user.
type jellyfinItem struct {
	ID                string            `json:"Id"`
	Type              string            `json:"Type"`
	SeriesID          string            `json:"SeriesId"`
	ParentIndexNumber int64             `json:"ParentIndexNumber"`
	IndexNumber       int64             `json:"IndexNumber"`
	RunTimeTicks      int64             `json:"RunTimeTicks"`
	ProviderIDs       map[string]string `json:"ProviderIds"`
	UserData          struct {
		Played                bool   `json:"Played"`
		PlaybackPositionTicks int64  `json:"PlaybackPositionTicks"`
		LastPlayedDate        string `json:"LastPlayedDate"`
	} `json:"UserData"`
}

// providerID reads a provider's id whatever case the server wrote its name in.
func providerID(it jellyfinItem, provider string) int64 {
	for name, id := range it.ProviderIDs {
		if strings.EqualFold(name, provider) {
			n, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return 0
			}
			return n
		}
	}
	return 0
}

func jellyfinSeen(it jellyfinItem) bool {
	return it.UserData.Played || it.UserData.PlaybackPositionTicks > 0
}

// jellyfinWatch is false for a title Jellyfin has no TMDB/TVDB id for. An
// episode takes its series' TVDB id, a movie passes 0.
func jellyfinWatch(email string, it jellyfinItem, seriesTvdb int64) (item, bool) {
	at := time.Unix(0, 0)
	if t, err := time.Parse(
		time.RFC3339Nano,
		it.UserData.LastPlayedDate,
	); err == nil {
		at = t
	}
	out := item{
		Email:   email,
		Watched: it.UserData.PlaybackPositionTicks == 0,
		At:      at.UTC().Format(time.RFC3339),
	}
	if !out.Watched {
		out.Position = it.UserData.PlaybackPositionTicks / ticksPerSecond
		out.Duration = it.RunTimeTicks / ticksPerSecond
	}
	if it.Type != "Episode" {
		out.TmdbID = providerID(it, "Tmdb")
		return out, out.TmdbID > 0
	}
	out.TvdbID = seriesTvdb
	out.Season, out.Episode = it.ParentIndexNumber, it.IndexNumber
	return out, out.TvdbID > 0
}

type jellyfin struct{ url, token string }

func (j jellyfin) get(path string, v any) error {
	header := http.Header{}
	// Jellyfin 10.11 answers 401 to X-Emby-Token alone; Emby reads only that.
	header.Set("Authorization", fmt.Sprintf("MediaBrowser Token=%q", j.token))
	header.Set("X-Emby-Token", j.token)
	return getJSON(j.url+path, header, v)
}

// accounts maps a Streamline email to each Jellyfin user's id. Jellyfin keeps
// no email, so the email comes from emails (name to email), or from a user
// whose name is one.
func (j jellyfin) accounts(emails map[string]string) (map[string]string, error) {
	var users []struct {
		ID   string `json:"Id"`
		Name string `json:"Name"`
	}
	if err := j.get("/Users", &users); err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, u := range users {
		email := emails[u.Name]
		if email == "" && strings.Contains(u.Name, "@") {
			email = u.Name
		}
		if email == "" {
			fmt.Fprintf(os.Stderr,
				"skipping Jellyfin user %q: pass -user %s=<email>\n", u.Name, u.Name)
			continue
		}
		ids[strings.ToLower(email)] = u.ID
	}
	return ids, nil
}

func (j jellyfin) list(userID, types string) ([]jellyfinItem, error) {
	q := url.Values{
		"userId":           {userID},
		"Recursive":        {"true"},
		"IncludeItemTypes": {types},
		"Fields":           {"ProviderIds"},
		"EnableUserData":   {"true"},
	}
	var out struct {
		Items []jellyfinItem `json:"Items"`
	}
	err := j.get("/Items?"+q.Encode(), &out)
	return out.Items, err
}

// watchState reads what one user has seen in every library they can reach.
func (j jellyfin) watchState(email, userID string) ([]item, int, error) {
	shows, err := j.list(userID, "Series")
	if err != nil {
		return nil, 0, err
	}
	series := make(map[string]int64, len(shows))
	for _, sh := range shows {
		series[sh.ID] = providerID(sh, "Tvdb")
	}
	// ponytail: every movie and episode in one answer, filtered here; page
	// with StartIndex/Limit if a library is too big to list.
	played, err := j.list(userID, "Movie,Episode")
	if err != nil {
		return nil, 0, err
	}
	var (
		items []item
		noID  int
	)
	for _, it := range played {
		if !jellyfinSeen(it) {
			continue
		}
		if w, ok := jellyfinWatch(email, it, series[it.SeriesID]); ok {
			items = append(items, w)
		} else {
			noID++
		}
	}
	return items, noID, nil
}
