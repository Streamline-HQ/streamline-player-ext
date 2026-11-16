package harness

import (
	"encoding/json"
	"fmt"
	"os"
)

// Library is the seed file: the shape of dev/mock-library.json, of which the
// harness reads only what Streamline's core tables hold.
type Library struct {
	Movies []Movie           `json:"movies"`
	Series []Series          `json:"series"`
	People map[string]Person `json:"people"`
}

type Movie struct {
	ID            int      `json:"id"`
	Title         string   `json:"title"`
	OriginalTitle string   `json:"original_title"`
	Year          int      `json:"year"`
	Overview      string   `json:"overview"`
	Runtime       int      `json:"runtime"`
	TmdbID        int      `json:"tmdb_id"`
	Genres        []string `json:"genres"`
	Rating        float64  `json:"rating"`
	Certification string   `json:"certification"`
	AddedAt       string   `json:"added_at"`
	Cast          []Cast   `json:"cast"`
}

type Cast struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Character string `json:"character"`
}

type Series struct {
	ID            int      `json:"id"`
	Title         string   `json:"title"`
	OriginalTitle string   `json:"original_title"`
	Year          int      `json:"year"`
	Overview      string   `json:"overview"`
	Network       string   `json:"network"`
	Rating        float64  `json:"rating"`
	Certification string   `json:"certification"`
	Genres        []string `json:"genres"`
	AddedAt       string   `json:"added_at"`
	Seasons       []Season `json:"seasons"`
	Cast          []Cast   `json:"cast"`
}

type Season struct {
	Number   int       `json:"number"`
	Name     string    `json:"name"`
	Episodes []Episode `json:"episodes"`
}

type Episode struct {
	Number   int    `json:"number"`
	Title    string `json:"title"`
	Overview string `json:"overview"`
	AirDate  string `json:"air_date"`
	Runtime  int    `json:"runtime"`
	HasFile  bool   `json:"has_file"`
}

type Person struct {
	Name string `json:"name"`
}

// LoadLibrary reads a seed file.
func LoadLibrary(path string) (Library, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Library{}, err
	}
	var lib Library
	if err := json.Unmarshal(raw, &lib); err != nil {
		return Library{}, fmt.Errorf("%s: %w", path, err)
	}
	return lib, nil
}
