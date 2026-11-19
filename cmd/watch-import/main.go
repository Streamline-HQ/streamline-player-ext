// Command watch-import copies every user's watch state (watched, resume
// position, last viewed) from Plex or Jellyfin into the player, through POST
// /admin/watchstate/import.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
)

const batchSize = 500

type item struct {
	Email    string `json:"email"`
	TmdbID   int64  `json:"tmdb_id,omitempty"`
	TvdbID   int64  `json:"tvdb_id,omitempty"`
	Season   int64  `json:"season,omitempty"`
	Episode  int64  `json:"episode,omitempty"`
	Position int64  `json:"position_seconds"`
	Duration int64  `json:"duration_seconds"`
	Watched  bool   `json:"watched"`
	At       string `json:"at"`
}

type report struct {
	Imported      int      `json:"imported"`
	Older         int      `json:"older"`
	UnknownTitles int      `json:"unknown_titles"`
	UnknownUsers  []string `json:"unknown_users"`
}

func post(player, token string, items []item) (report, error) {
	var total report
	for batch := range slices.Chunk(items, batchSize) {
		body, err := json.Marshal(map[string]any{"items": batch})
		if err != nil {
			return total, err
		}
		req, err := http.NewRequest(
			http.MethodPost,
			strings.TrimRight(player, "/")+"/admin/watchstate/import",
			bytes.NewReader(body),
		)
		if err != nil {
			return total, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return total, err
		}
		raw, err := io.ReadAll(resp.Body)
		if closeErr := resp.Body.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return total, err
		}
		if resp.StatusCode != http.StatusOK {
			return total, fmt.Errorf("import: %s: %s", resp.Status, raw)
		}
		var r report
		if err := json.Unmarshal(raw, &r); err != nil {
			return total, err
		}
		total.Imported += r.Imported
		total.Older += r.Older
		total.UnknownTitles += r.UnknownTitles
		for _, u := range r.UnknownUsers {
			if !slices.Contains(total.UnknownUsers, u) {
				total.UnknownUsers = append(total.UnknownUsers, u)
			}
		}
	}
	return total, nil
}

// get answers a 200's body to decode; anything else is an error naming the
// path, never the query, which may carry a token.
func get(url string, header http.Header, decode func(io.Reader) error) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header = header
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", req.URL.Path, resp.Status)
	}
	return decode(resp.Body)
}

func getJSON(url string, header http.Header, v any) error {
	header.Set("Accept", "application/json")
	return get(url, header, func(r io.Reader) error {
		return json.NewDecoder(r).Decode(v)
	})
}

// userFlag collects -user name=email pairs.
type userFlag map[string]string

func (u userFlag) String() string { return fmt.Sprint(map[string]string(u)) }

func (u userFlag) Set(v string) error {
	name, email, ok := strings.Cut(v, "=")
	if !ok || name == "" || email == "" {
		return errors.New("want name=email")
	}
	u[name] = email
	return nil
}

func run() error {
	plexURL := flag.String("plex", "", "Plex server URL (http://plex.lan:32400)")
	jellyfinURL := flag.String(
		"jellyfin", "", "Jellyfin server URL (http://jellyfin.lan:8096)",
	)
	users := userFlag{}
	flag.Var(users, "user",
		"a Jellyfin user's Streamline email, as name=email; repeat per user")
	player := flag.String(
		"player", "", "player API URL (https://streamline.lan/extensions/player)",
	)
	dryRun := flag.Bool(
		"dry-run",
		false,
		"read the servers and print, write nothing",
	)
	flag.Parse()
	plexToken, jellyfinToken := os.Getenv("PLEX_TOKEN"), os.Getenv("JELLYFIN_TOKEN")
	playerToken := os.Getenv("PLAYER_TOKEN")
	switch {
	case *plexURL == "" && *jellyfinURL == "":
		return errors.New("need -plex or -jellyfin")
	case *plexURL != "" && plexToken == "":
		return errors.New("-plex needs PLEX_TOKEN, the server owner's token")
	case *jellyfinURL != "" && jellyfinToken == "":
		return errors.New("-jellyfin needs JELLYFIN_TOKEN, an API key")
	case !*dryRun && (*player == "" || playerToken == ""):
		return errors.New(
			"need -player and PLAYER_TOKEN (an admin's access token), or -dry-run",
		)
	}

	var all []item
	collect := func(
		accounts map[string]string,
		read func(email, credential string) ([]item, int, error),
	) error {
		for email, credential := range accounts {
			items, noID, err := read(email, credential)
			if err != nil {
				return fmt.Errorf("%s: %w", email, err)
			}
			fmt.Printf("%s: %d seen, %d without a TMDB/TVDB id\n",
				email, len(items), noID)
			all = append(all, items...)
		}
		return nil
	}
	if *plexURL != "" {
		p := plex{url: strings.TrimRight(*plexURL, "/")}
		accounts, err := p.accounts(plexToken)
		if err != nil {
			return err
		}
		if err := collect(accounts, p.watchState); err != nil {
			return err
		}
	}
	if *jellyfinURL != "" {
		j := jellyfin{
			url: strings.TrimRight(*jellyfinURL, "/"), token: jellyfinToken,
		}
		accounts, err := j.accounts(users)
		if err != nil {
			return err
		}
		if err := collect(accounts, j.watchState); err != nil {
			return err
		}
	}
	if *dryRun {
		return nil
	}
	r, err := post(*player, playerToken, all)
	if err != nil {
		return err
	}
	fmt.Printf(
		"%d files written, %d older than the player's, %d titles not in "+
			"Streamline, no Streamline user for %v\n",
		r.Imported, r.Older, r.UnknownTitles, r.UnknownUsers,
	)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
