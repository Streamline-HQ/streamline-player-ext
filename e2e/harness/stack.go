// Package harness boots a real Streamline with the player extension, an
// optional engine and a seeded library. The e2e specs, `go run ./e2e/cmd/stack`
// and the contract checks all start their stack through it.
package harness

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

const (
	ExtensionName = "player"
	// MountBase is where Streamline serves the extension.
	MountBase = "/extensions/" + ExtensionName

	AdminEmail    = "e2e-admin@streamline.local"
	AdminPassword = "e2e-Admin-Passw0rd!"
	UserPassword  = "e2e-Login-Passw0rd!"

	// ScratchRoot holds one directory per booted stack, removed on Close.
	ScratchRoot = "/tmp/streamline-player-ext/e2e"

	sessionCookie = "streamline_session"
)

// Options say what to boot. The zero value boots Streamline and the extension
// on the small library in testdata/library.json, with no engine.
type Options struct {
	// Library is a seed file; testdata/library.json when empty.
	Library string
	// ArtDir is copied into Streamline's poster folder when set.
	ArtDir string
	// Port is Streamline's port; a free one when zero.
	Port int
	// Engine starts the engine named by ENGINE_BIN on the CPU backend.
	Engine bool
	// Users are members created after boot, named by their email prefix.
	Users []string
	// Log receives the output of the processes; discarded when nil.
	Log io.Writer
}

type User struct {
	ID       uint32 `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	Password string `json:"password"`
}

// Stack is a running Streamline, with its engine when asked for one.
type Stack struct {
	BaseURL   string
	EngineURL string
	// EngineSecret is the engine's shared secret; empty without an engine.
	EngineSecret string
	DataDir      string
	// Dir is the stack's scratch directory; Close removes it.
	Dir   string
	Admin User
	// Users are the members created so far.
	Users []User
	Core  *sql.DB
	// AdminToken is the admin's Streamline session, for /api/v1.
	AdminToken string
	// Seeded lists what the library file put on disk.
	Seeded Seeded

	log     io.Writer
	procs   []*proc
	counter int
	closed  bool
	// sessions caches each user's Streamline session, since the login
	// endpoint allows only a few attempts per address.
	sessions map[string]string
}

type proc struct {
	name   string
	cmd    *exec.Cmd
	exited chan struct{}
}

// Boot starts the stack and panics with the reason when it cannot.
func Boot(opts Options) *Stack {
	s, err := boot(opts)
	if err != nil {
		if s != nil {
			s.Close()
		}
		panic(fmt.Errorf("harness: %w", err))
	}
	return s
}

func boot(opts Options) (*Stack, error) {
	bin, err := needFile("STREAMLINE_BIN", "a built streamline binary")
	if err != nil {
		return nil, err
	}
	wasm, err := findWasm()
	if err != nil {
		return nil, err
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, errors.New("the stack needs a real ffmpeg on PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return nil, errors.New("the stack needs ffprobe on PATH")
	}
	libPath := opts.Library
	if libPath == "" {
		libPath = defaultLibrary()
	}
	lib, err := LoadLibrary(libPath)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(ScratchRoot, 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(ScratchRoot, "run-")
	if err != nil {
		return nil, err
	}
	s := &Stack{Dir: dir, DataDir: filepath.Join(dir, "data"), log: opts.Log}
	if s.log == nil {
		s.log = io.Discard
	}
	for _, d := range []string{
		"movies", "series", "music", "ebooks", "audiobooks", "downloads",
	} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return s, err
		}
	}

	port := opts.Port
	if port == 0 {
		if port, err = freePort(); err != nil {
			return s, err
		}
	}
	s.BaseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	var enginePort int
	if opts.Engine {
		enginePort, err = freePort()
		if err != nil {
			return s, err
		}
		s.EngineURL = fmt.Sprintf("http://127.0.0.1:%d", enginePort)
	}

	cfgPath := filepath.Join(dir, "config.yaml")
	if err := s.writeConfig(
		cfgPath,
		port,
		enginePort,
		wasm,
		filepath.Dir(ffmpeg),
	); err != nil {
		return s, err
	}
	if opts.Engine {
		if err := s.startEngine(enginePort, filepath.Dir(ffmpeg)); err != nil {
			return s, err
		}
	}
	if err := s.start("streamline", bin, nil, "--config", cfgPath); err != nil {
		return s, err
	}
	if err := waitStatus(s.BaseURL+"/health", "", http.StatusOK); err != nil {
		return s, err
	}
	if s.AdminToken, err = s.Login(AdminEmail, AdminPassword); err != nil {
		return s, err
	}
	s.Admin = User{
		ID: 1, Name: "e2e-admin", Email: AdminEmail, Role: "admin",
		Password: AdminPassword,
	}
	if err := s.approveExtension(); err != nil {
		return s, err
	}

	core, err := sql.Open(
		"sqlite",
		"file:"+filepath.Join(s.DataDir, "streamline.db")+
			"?_pragma=busy_timeout(10000)",
	)
	if err != nil {
		return s, err
	}
	s.Core = core
	if err := s.seed(lib, opts.ArtDir); err != nil {
		return s, fmt.Errorf("seeding the library: %w", err)
	}
	for _, name := range opts.Users {
		if _, err := s.newUser(name); err != nil {
			return s, err
		}
	}
	return s, nil
}

// Ext opens the extension's own database. The caller closes it.
func (s *Stack) Ext() *sql.DB {
	db, err := sql.Open("sqlite", "file:"+s.ExtPath()+"?_pragma=busy_timeout(10000)")
	if err != nil {
		panic(fmt.Errorf("harness: %w", err))
	}
	return db
}

func (s *Stack) ExtPath() string {
	return filepath.Join(s.DataDir, "extensions", ExtensionName, "data.db")
}

// NewUser creates a member account through the admin API.
func (s *Stack) NewUser(name string) User {
	u, err := s.newUser(name)
	if err != nil {
		panic(fmt.Errorf("harness: %w", err))
	}
	return u
}

// NewNamedUser is NewUser with a display name on the account.
func (s *Stack) NewNamedUser(name, displayName string) User {
	u, err := s.createUser(name, displayName)
	if err != nil {
		panic(fmt.Errorf("harness: %w", err))
	}
	return u
}

func (s *Stack) newUser(name string) (User, error) {
	return s.createUser(name, "")
}

func (s *Stack) createUser(name, displayName string) (User, error) {
	s.counter++
	email := fmt.Sprintf("%s-%d@e2e.local", strings.ToLower(name), s.counter)
	var created struct {
		ID uint32 `json:"id"`
	}
	status, raw, err := s.Do(
		http.MethodPost,
		"/api/v1/users",
		s.AdminToken,
		map[string]any{
			"email": email, "password": UserPassword, "role": "member",
			"display_name": displayName,
		},
	)
	if err != nil {
		return User{}, err
	}
	if status != http.StatusCreated {
		return User{}, fmt.Errorf("creating %s: %d %s", email, status, raw)
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		return User{}, err
	}
	u := User{
		ID: created.ID, Name: name, Email: email, Role: "member",
		Password: UserPassword,
	}
	s.Users = append(s.Users, u)
	return u, nil
}

// Login signs in through Streamline's web endpoint and returns the session
// JWT, which /api/v1 takes as a Bearer token. The endpoint allows 5 attempts
// per 15 minutes per address.
func (s *Stack) Login(email, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, err := http.NewRequest(
		http.MethodPost, s.BaseURL+"/auth/login", bytes.NewReader(body),
	)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return "", fmt.Errorf("login as %s: %s", email, resp.Status)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			return c.Value, nil
		}
	}
	return "", errors.New("the login response did not set the session cookie")
}

var client = &http.Client{Timeout: 30 * time.Second}

// Do sends one request to Streamline. token is a session JWT, empty for none;
// a non-nil body is sent as JSON.
func (s *Stack) Do(
	method, path, token string,
	body any,
) (status int, raw []byte, err error) {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.BaseURL+path, payload)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err = io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

func (s *Stack) approveExtension() error {
	status, raw, err := s.Do(
		http.MethodPost, "/api/v1/extensions/"+ExtensionName+"/approve",
		s.AdminToken, nil,
	)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("approving the extension: %d %s", status, raw)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		state, err := s.extensionState()
		if err != nil {
			return err
		}
		if state == "running" {
			return s.touchExtension()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("the extension did not start")
}

func (s *Stack) extensionState() (string, error) {
	status, raw, err := s.Do(
		http.MethodGet, "/api/v1/extensions", s.AdminToken, nil,
	)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("listing extensions: %d %s", status, raw)
	}
	var list struct {
		Items []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", err
	}
	for _, it := range list.Items {
		if it.Name == ExtensionName {
			return it.State, nil
		}
	}
	return "", errors.New("the extension is not listed")
}

// touchExtension makes the extension create its tables, which the helpers
// that read its database need.
func (s *Stack) touchExtension() error {
	resp, err := client.Get(s.BaseURL + MountBase + "/")
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (s *Stack) writeConfig(
	path string,
	port, enginePort int,
	wasm, ffmpegDir string,
) error {
	hosts := []string{fmt.Sprintf("127.0.0.1:%d", port)}
	if enginePort != 0 {
		hosts = append(hosts, fmt.Sprintf("127.0.0.1:%d", enginePort))
	}
	cfg := map[string]any{
		"data_dir": s.DataDir,
		"server":   map[string]any{"host": "127.0.0.1", "port": port},
		"library": map[string]any{
			"movie_path":     filepath.Join(s.Dir, "movies"),
			"series_path":    filepath.Join(s.Dir, "series"),
			"music_path":     filepath.Join(s.Dir, "music"),
			"ebook_path":     filepath.Join(s.Dir, "ebooks"),
			"audiobook_path": filepath.Join(s.Dir, "audiobooks"),
			"download_path":  filepath.Join(s.Dir, "downloads"),
		},
		"auth": map[string]any{
			"session_secret":    "e2e-session-secret",
			"api_failure_limit": "0",
			"seed_admin": map[string]any{
				"email":    AdminEmail,
				"password": AdminPassword,
			},
		},
		"ffmpeg": map[string]any{"enabled": true, "path": ffmpegDir},
		"log": map[string]any{
			"app":  map[string]any{"level": "warn"},
			"http": map[string]any{"enabled": false},
		},
		"extensions": []map[string]any{{
			"name":    ExtensionName,
			"source":  wasm,
			"enabled": true,
			"hosts":   hosts,
		}},
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func (s *Stack) startEngine(port int, ffmpegDir string) error {
	bin, err := needFile("ENGINE_BIN", "a built engine binary")
	if err != nil {
		return err
	}
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	s.EngineSecret = base64.RawURLEncoding.EncodeToString(secret)
	cfg := fmt.Sprintf(
		"listen: 127.0.0.1:%d\nsecret: %s\ntmp_dir: %s\njobs_dir: %s\n"+
			"ffmpeg_dir: %s\nhw_accel: none\n",
		port, s.EngineSecret,
		filepath.Join(s.Dir, "engine", "live"),
		filepath.Join(s.Dir, "engine", "jobs"),
		ffmpegDir,
	)
	path := filepath.Join(s.Dir, "engine.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		return err
	}
	if err := s.start(
		"engine",
		bin,
		[]string{"RUST_LOG=warn"},
		"--config",
		path,
	); err != nil {
		return err
	}
	return waitStatus(s.EngineURL+"/v1/health", s.EngineSecret, http.StatusOK)
}

// start runs a process with a clean environment: nothing of the developer's
// STREAMLINE_* settings reaches it.
func (s *Stack) start(name, bin string, env []string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + s.Dir,
		"TMPDIR=" + s.Dir,
	}, env...)
	cmd.Stdout = s.log
	cmd.Stderr = s.log
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", name, err)
	}
	p := &proc{name: name, cmd: cmd, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.exited)
	}()
	s.procs = append(s.procs, p)
	return nil
}

// Close stops every process and removes the scratch directory.
func (s *Stack) Close() {
	if s.closed {
		return
	}
	s.closed = true
	if s.Core != nil {
		_ = s.Core.Close()
	}
	for _, p := range s.procs {
		_ = p.cmd.Process.Kill()
		<-p.exited
	}
	_ = os.RemoveAll(s.Dir)
}

func needFile(env, what string) (string, error) {
	path := os.Getenv(env)
	if path == "" {
		return "", fmt.Errorf("%s must name %s", env, what)
	}
	return path, nil
}

// findWasm takes PLAYER_WASM, else player.wasm in the working directory or the
// one above it (the spec runs in e2e/, the stack command at the root).
func findWasm() (string, error) {
	if p := os.Getenv("PLAYER_WASM"); p != "" {
		return p, nil
	}
	for _, p := range []string{"player.wasm", "../player.wasm"} {
		if abs, err := filepath.Abs(p); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs, nil
			}
		}
	}
	return "", errors.New("build player.wasm first (task build) or set PLAYER_WASM")
}

func defaultLibrary() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "testdata", "library.json")
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitStatus(url, bearer string, want int) error {
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		resp, err := getWithBearer(url, bearer)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == want {
				return nil
			}
			last = fmt.Errorf("%s answered %d", url, resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("waiting for %s: %w", url, last)
}

func getWithBearer(url, bearer string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return client.Do(req)
}
