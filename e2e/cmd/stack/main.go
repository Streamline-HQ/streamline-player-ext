// Command stack boots Streamline, the player extension and the engine on a
// seeded library, prints one JSON line with where they are and the logins, and
// stays up until it is interrupted.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/streamline-hq/streamline-player-ext/e2e/harness"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (err error) {
	library := flag.String(
		"library",
		"",
		"seed file (default: the small test library)",
	)
	art := flag.String("art", "", "directory copied into the poster folder")
	port := flag.Int("port", 0, "Streamline's port (default: a free one)")
	flag.Parse()

	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%v", p)
		}
	}()
	stack := harness.Boot(harness.Options{
		Library: *library, ArtDir: *art, Port: *port,
		Engine: os.Getenv("ENGINE_BIN") != "",
		Users:  []string{"ana", "ben"},
		Log:    os.Stderr,
	})
	defer stack.Close()

	line, err := json.Marshal(map[string]any{
		"url":    stack.BaseURL,
		"api":    stack.BaseURL + harness.MountBase,
		"engine": stack.EngineURL,
		"admin": map[string]string{
			"email": harness.AdminEmail, "password": harness.AdminPassword,
		},
		"users": stack.Users,
	})
	if err != nil {
		return err
	}
	fmt.Println(string(line))

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	return nil
}
