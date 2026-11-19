package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

const plexTV = "https://plex.tv"

// plexMeta is a movie, show or episode as a section listing gives it; times
// are in milliseconds.
type plexMeta struct {
	GUID  string `json:"guid"`
	Guids []struct {
		ID string `json:"id"`
	} `json:"Guid"`
	RatingKey            string `json:"ratingKey"`
	GrandparentRatingKey string `json:"grandparentRatingKey"`
	ParentIndex          int64  `json:"parentIndex"`
	Index                int64  `json:"index"`
	ViewCount            int64  `json:"viewCount"`
	ViewOffset           int64  `json:"viewOffset"`
	LastViewedAt         int64  `json:"lastViewedAt"`
	Duration             int64  `json:"duration"`
}

var legacyAgents = map[string]string{
	"tmdb": "themoviedb://",
	"tvdb": "thetvdb://",
}

// externalID reads source's id from the Guid list, or from the guid of an
// item matched by a legacy agent ("com.plexapp.agents.thetvdb://73739?lang=en").
func externalID(m plexMeta, source string) int64 {
	id := ""
	for _, g := range m.Guids {
		if rest, ok := strings.CutPrefix(g.ID, source+"://"); ok {
			id = rest
		}
	}
	if _, rest, ok := strings.Cut(m.GUID, legacyAgents[source]); ok && id == "" {
		id = rest
		if end := strings.IndexFunc(rest, func(r rune) bool {
			return r < '0' || r > '9'
		}); end >= 0 {
			id = rest[:end]
		}
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func plexSeen(m plexMeta) bool { return m.ViewCount > 0 || m.ViewOffset > 0 }

// plexItem is false for a title Plex has no TMDB/TVDB id for. An episode takes
// its series' TVDB id, a movie passes 0.
func plexItem(email string, m plexMeta, seriesTvdb int64) (item, bool) {
	it := item{
		Email:   email,
		Watched: m.ViewOffset == 0,
		At:      time.Unix(m.LastViewedAt, 0).UTC().Format(time.RFC3339),
	}
	if !it.Watched {
		it.Position, it.Duration = m.ViewOffset/1000, m.Duration/1000
	}
	if m.GrandparentRatingKey == "" {
		it.TmdbID = externalID(m, "tmdb")
		return it, it.TmdbID > 0
	}
	it.TvdbID, it.Season, it.Episode = seriesTvdb, m.ParentIndex, m.Index
	return it, it.TvdbID > 0
}

type plex struct{ url string }

type plexUser struct {
	ID    int64  `xml:"id,attr"`
	Email string `xml:"email,attr"`
	Title string `xml:"title,attr"`
}

func plexGet(url, token string, v any) error {
	header := http.Header{}
	header.Set("X-Plex-Token", token)
	header.Set("X-Plex-Client-Identifier", "streamline-watch-import")
	if strings.HasPrefix(url, plexTV+"/api/") &&
		!strings.HasPrefix(url, plexTV+"/api/v2/") {
		return get(url, header, func(r io.Reader) error {
			return xml.NewDecoder(r).Decode(v)
		})
	}
	return getJSON(url, header, v)
}

func (p plex) list(token, path string) ([]plexMeta, error) {
	var out struct {
		MediaContainer struct {
			Metadata []plexMeta `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	err := plexGet(p.url+path, token, &out)
	return out.MediaContainer.Metadata, err
}

// accounts maps each account's email to its token for this server: the owner
// and everyone the server is shared with.
func (p plex) accounts(ownerToken string) (map[string]string, error) {
	var identity struct {
		MediaContainer struct {
			MachineIdentifier string `json:"machineIdentifier"`
		} `json:"MediaContainer"`
	}
	if err := plexGet(p.url+"/identity", ownerToken, &identity); err != nil {
		return nil, err
	}
	var owner struct {
		Email string `json:"email"`
	}
	if err := plexGet(plexTV+"/api/v2/user", ownerToken, &owner); err != nil {
		return nil, err
	}
	var users struct {
		Users []plexUser `xml:"User"`
	}
	if err := plexGet(plexTV+"/api/users/", ownerToken, &users); err != nil {
		return nil, err
	}
	var shares struct {
		Shared []struct {
			UserID int64  `xml:"userID,attr"`
			Token  string `xml:"accessToken,attr"`
		} `xml:"SharedServer"`
	}
	if err := plexGet(
		plexTV+"/api/servers/"+identity.MediaContainer.MachineIdentifier+
			"/shared_servers", ownerToken, &shares,
	); err != nil {
		return nil, err
	}

	tokens := map[string]string{strings.ToLower(owner.Email): ownerToken}
	for _, s := range shares.Shared {
		i := slices.IndexFunc(users.Users, func(u plexUser) bool {
			return u.ID == s.UserID
		})
		switch {
		case i < 0:
			fmt.Fprintf(os.Stderr, "skipping Plex user %d: not listed\n", s.UserID)
		case users.Users[i].Email == "":
			// A managed home user has no email to find a Streamline user by.
			fmt.Fprintf(os.Stderr, "skipping %q: no email\n", users.Users[i].Title)
		default:
			tokens[strings.ToLower(users.Users[i].Email)] = s.Token
		}
	}
	return tokens, nil
}

// watchState reads what one account has seen in every section it can reach.
func (p plex) watchState(email, token string) ([]item, int, error) {
	var sections struct {
		MediaContainer struct {
			Directory []struct {
				Key  string `json:"key"`
				Type string `json:"type"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := plexGet(p.url+"/library/sections", token, &sections); err != nil {
		return nil, 0, err
	}
	var (
		items []item
		noID  int
	)
	add := func(metas []plexMeta, series map[string]int64) {
		for _, m := range metas {
			if !plexSeen(m) {
				continue
			}
			if it, ok := plexItem(email, m, series[m.GrandparentRatingKey]); ok {
				items = append(items, it)
			} else {
				noID++
			}
		}
	}
	for _, s := range sections.MediaContainer.Directory {
		// ponytail: whole sections, filtered here; ask Plex for
		// viewCount>=1 and inProgress=1 if a library is too big to list.
		all := "/library/sections/" + s.Key + "/all?includeGuids=1&type="
		switch s.Type {
		case "movie":
			movies, err := p.list(token, all+"1")
			if err != nil {
				return nil, 0, err
			}
			add(movies, nil)
		case "show":
			shows, err := p.list(token, all+"2")
			if err != nil {
				return nil, 0, err
			}
			series := make(map[string]int64, len(shows))
			for _, sh := range shows {
				series[sh.RatingKey] = externalID(sh, "tvdb")
			}
			episodes, err := p.list(token, all+"4")
			if err != nil {
				return nil, 0, err
			}
			add(episodes, series)
		}
	}
	return items, noID, nil
}
