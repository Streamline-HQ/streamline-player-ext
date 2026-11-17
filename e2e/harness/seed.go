package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	clipSeconds = 8
	subtitles   = "1\n00:00:01,000 --> 00:00:03,000\nHello\n\n" +
		"2\n00:00:04,000 --> 00:00:06,000\nWorld\n"
	chapters = ";FFMETADATA1\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=2000\n" +
		"title=Intro\n"
)

// Seeded is what the seed put on disk and in core.
type Seeded struct {
	Movies   []SeededFile
	Episodes []SeededFile
}

// SeededFile is one video file and the row that owns it.
type SeededFile struct {
	Title   string
	OwnerID int64
	FileID  int64
	Path    string
}

// coreTime is a time the way the core database stores one.
func coreTime(t time.Time) string { return t.UTC().String() }

func parseTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Now()
}

func (s *Stack) seed(lib Library, artDir string) error {
	clip, err := s.makeClip()
	if err != nil {
		return err
	}
	people := map[string]int64{}
	person := func(c Cast, known map[string]Person) (int64, error) {
		if id, ok := people[c.ID]; ok {
			return id, nil
		}
		name := c.Name
		if p, ok := known[c.ID]; ok && p.Name != "" {
			name = p.Name
		}
		now := coreTime(time.Now())
		res, err := s.Core.Exec(
			`INSERT INTO persons(create_time, update_time, tmdb_id, tvdb_id, name)
			VALUES(?, ?, 0, 0, ?)`, now, now, name)
		if err != nil {
			return 0, err
		}
		id, err := res.LastInsertId()
		people[c.ID] = id
		return id, err
	}
	credit := func(c Cast, column string, owner int64, order int) error {
		pid, err := person(c, lib.People)
		if err != nil {
			return err
		}
		now := coreTime(time.Now())
		_, err = s.Core.Exec(
			`INSERT INTO credits(create_time, update_time, character, "order",
				job, department, person_credits, `+column+`)
			VALUES(?, ?, ?, ?, '', 'Acting', ?, ?)`,
			now, now, c.Character, order, pid, owner)
		return err
	}

	for _, m := range lib.Movies {
		genres, _ := json.Marshal(m.Genres)
		added := coreTime(parseTime(m.AddedAt))
		res, err := s.Core.Exec(
			`INSERT INTO movies(create_time, update_time, title, original_title,
				year, overview, runtime, status, monitored, tmdb_id,
				certification, grab_failures, rating, genres)
			VALUES(?, ?, ?, ?, ?, ?, ?, 'available', 1, ?, ?, 0, ?, ?)`,
			added, added, m.Title, orElse(m.OriginalTitle, m.Title), m.Year,
			m.Overview, m.Runtime, m.TmdbID, m.Certification, m.Rating,
			string(genres))
		if err != nil {
			return fmt.Errorf("movie %q: %w", m.Title, err)
		}
		id, _ := res.LastInsertId()
		for n, c := range m.Cast {
			if err := credit(c, "movie_credits", id, n); err != nil {
				return err
			}
		}
		name := fmt.Sprintf("%s (%d)", safeName(m.Title), m.Year)
		path := filepath.Join(s.Dir, "movies", name, name+".mkv")
		fid, err := s.addFile(clip, path, "movie_media_files", id, m.AddedAt)
		if err != nil {
			return err
		}
		s.Seeded.Movies = append(s.Seeded.Movies, SeededFile{
			Title: m.Title, OwnerID: id, FileID: fid, Path: path,
		})
	}

	for _, sr := range lib.Series {
		genres, _ := json.Marshal(sr.Genres)
		added := coreTime(parseTime(sr.AddedAt))
		res, err := s.Core.Exec(
			`INSERT INTO tv_shows(create_time, update_time, title, original_title,
				year, overview, series_status, type, monitored, tvdb_id, network,
				certification, runtime, rating, genres)
			VALUES(?, ?, ?, ?, ?, ?, 'continuing', 'standard', 1, ?, ?, ?, 0, ?, ?)`,
			added, added, sr.Title, orElse(sr.OriginalTitle, sr.Title), sr.Year,
			sr.Overview, sr.ID, sr.Network, sr.Certification, sr.Rating,
			string(genres))
		if err != nil {
			return fmt.Errorf("series %q: %w", sr.Title, err)
		}
		showID, _ := res.LastInsertId()
		for n, c := range sr.Cast {
			if err := credit(c, "tv_show_credits", showID, n); err != nil {
				return err
			}
		}
		for _, se := range sr.Seasons {
			res, err := s.Core.Exec(
				`INSERT INTO seasons(create_time, update_time, number, name,
					monitored, monitor_pinned, tv_show_seasons)
				VALUES(?, ?, ?, ?, 1, 0, ?)`,
				added, added, se.Number, se.Name, showID)
			if err != nil {
				return err
			}
			seasonID, _ := res.LastInsertId()
			for _, ep := range se.Episodes {
				status := "wanted"
				if ep.HasFile {
					status = "available"
				}
				res, err := s.Core.Exec(
					`INSERT INTO episodes(create_time, update_time, number, title,
						overview, air_date, monitored, monitor_pinned,
						absolute_number, grab_failures, tvdb_id, status,
						season_episodes)
					VALUES(?, ?, ?, ?, ?, ?, 1, 0, 0, 0, 0, ?, ?)`,
					added, added, ep.Number, ep.Title, ep.Overview,
					coreTime(parseTime(ep.AirDate+"T00:00:00Z")), status, seasonID)
				if err != nil {
					return err
				}
				epID, _ := res.LastInsertId()
				if !ep.HasFile {
					continue
				}
				show := safeName(sr.Title)
				file := fmt.Sprintf(
					"%s - S%02dE%02d.mkv",
					show,
					se.Number,
					ep.Number,
				)
				path := filepath.Join(s.Dir, "series", show,
					fmt.Sprintf("Season %02d", se.Number), file)
				fid, err := s.addFile(
					clip,
					path,
					"episode_media_files",
					epID,
					sr.AddedAt,
				)
				if err != nil {
					return err
				}
				s.Seeded.Episodes = append(s.Seeded.Episodes, SeededFile{
					Title: ep.Title, OwnerID: epID, FileID: fid, Path: path,
				})
			}
		}
	}

	if artDir != "" {
		dst := filepath.Join(s.DataDir, "posters")
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		if out, err := exec.Command("cp", "-a", artDir+"/.", dst).
			CombinedOutput(); err != nil {
			return fmt.Errorf("copying artwork: %w: %s", err, out)
		}
	}
	return nil
}

// addFile hard-links the clip to path and records it against its owner.
func (s *Stack) addFile(
	clip, path, ownerColumn string,
	ownerID int64,
	addedAt string,
) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	if err := os.Link(clip, path); err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	now := coreTime(parseTime(addedAt))
	res, err := s.Core.Exec(
		`INSERT INTO media_files(create_time, update_time, path, size, quality,
			format, source, container, duration_seconds, video_codec, width,
			height, audio_codec, audio_channels, bitrate, audio_tracks,
			audio_langs, sub_langs, probed_at, bit_depth, tag_failures, `+
			ownerColumn+`)
		VALUES(?, ?, ?, ?, '360p', 'mkv', 'auto', 'matroska', ?, 'h264', 640,
			360, 'aac', 2, ?, 1, 'eng', 'eng', ?, 8, 0, ?)`,
		now, now, path, info.Size(), clipSeconds,
		info.Size()*8/clipSeconds/1000, now, ownerID)
	if err != nil {
		return 0, fmt.Errorf("file %s: %w", path, err)
	}
	return res.LastInsertId()
}

// makeClip generates the one clip every file is a link to: 8 s of video with
// a key frame every 2 s, one audio track, one SRT subtitle and an intro
// chapter, in mkv.
func (s *Stack) makeClip() (string, error) {
	srt := filepath.Join(s.Dir, "clip.srt")
	if err := os.WriteFile(srt, []byte(subtitles), 0o644); err != nil {
		return "", err
	}
	meta := filepath.Join(s.Dir, "clip.ffmeta")
	if err := os.WriteFile(meta, []byte(chapters), 0o644); err != nil {
		return "", err
	}
	clip := filepath.Join(s.Dir, "clip.mkv")
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440",
		"-i", srt,
		"-i", meta,
		"-t", fmt.Sprint(clipSeconds),
		"-map", "0:v", "-map", "1:a", "-map", "2:s", "-map_chapters", "3",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "48",
		"-c:a", "aac", "-c:s", "srt",
		"-metadata:s:a:0", "language=eng", "-metadata:s:s:0", "language=eng",
		clip).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("generating the clip: %w: %s", err, out)
	}
	return clip, nil
}

func orElse(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == ':' || r == 0 {
			return '_'
		}
		return r
	}, s)
}
