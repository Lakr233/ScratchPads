// Command scratchpads serves ScratchPads: shared markdown pads where people
// and their agents read, write and comment over a curl-friendly HTTPS API.
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"

	"github.com/Lakr233/ScratchPads/internal/app"
)

//go:embed web
var webFiles embed.FS

//go:embed skills/scratchpads/SKILL.md
var skillFile []byte

const usage = `usage: scratchpads <command>

  serve                     run the HTTPS server (default)
  pin                       print the TLS public key pin for curl --pinnedpubkey
  user <name> [--admin]     create a user and print its token
  agent <user> <slot>       create agent <user>/<slot> and print its token
  token <name>              issue another token for a user or agent
  grant <topic> <subject> <read|write|admin>
                            grant a user, user/slot or user/* access to a topic

Environment: SP_ADDR (:8443), SP_DATA (./data), SP_HOSTNAMES, SP_TLS_CERT,
SP_TLS_KEY, SP_TRUST_CLOUDFLARE=1
`

func main() {
	log.SetFlags(log.LstdFlags)
	cfg := app.ConfigFromEnv()
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	if err := run(cfg, cmd, args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(cfg app.Config, cmd string, args []string) error {
	switch cmd {
	case "serve":
		return serve(cfg)
	case "pin":
		cert, err := app.LoadOrCreateCert(cfg)
		if err != nil {
			return err
		}
		pin, err := app.PublicKeyPin(cert)
		if err != nil {
			return err
		}
		fmt.Println(pin)
		return nil
	case "user", "agent", "token", "grant":
		store, err := app.OpenDataStore(cfg)
		if err != nil {
			return err
		}
		defer store.Close()
		return admin(store, cmd, args)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	}
	fmt.Fprint(os.Stderr, usage)
	return fmt.Errorf("unknown command %q", cmd)
}

func serve(cfg app.Config) error {
	store, err := app.OpenDataStore(cfg)
	if err != nil {
		return err
	}
	defer store.Close()
	cert, err := app.LoadOrCreateCert(cfg)
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	pin, _ := app.PublicKeyPin(cert)
	if n, err := store.CountUsers(); err == nil && n == 0 {
		log.Printf("no users yet; create the first admin with: scratchpads user <name> --admin")
	}
	web, err := fs.Sub(webFiles, "web")
	if err != nil {
		return err
	}
	log.Printf("ScratchPads listening on https://%s (hosts %v)", cfg.Addr, cfg.Hostnames)
	log.Printf("certificate sha256 %s", app.CertFingerprint(cert))
	log.Printf("curl pin: --pinnedpubkey sha256//%s", pin)
	return app.NewServer(cfg, store, web, skillFile).ListenAndServe(cert)
}

func admin(store *app.Store, cmd string, args []string) error {
	switch {
	case cmd == "user" && len(args) >= 1:
		u, err := store.CreateUser(args[0], len(args) > 1 && args[1] == "--admin")
		if err != nil {
			return err
		}
		return printToken(store, u)
	case cmd == "agent" && len(args) == 2:
		owner, err := store.PrincipalByName(args[0])
		if err != nil {
			return fmt.Errorf("user %s: %w", args[0], err)
		}
		a, err := store.CreateAgent(owner, args[1])
		if err != nil {
			return err
		}
		return printToken(store, a)
	case cmd == "token" && len(args) == 1:
		p, err := store.PrincipalByName(args[0])
		if err != nil {
			return fmt.Errorf("%s: %w", args[0], err)
		}
		return printToken(store, p)
	case cmd == "grant" && len(args) == 3:
		t, err := store.TopicBySlug(args[0])
		if err != nil {
			return fmt.Errorf("topic %s: %w", args[0], err)
		}
		if err := store.SetGrant(t.ID, args[1], args[2]); err != nil {
			return err
		}
		fmt.Printf("%s can now %s %s\n", args[1], args[2], args[0])
		return nil
	}
	fmt.Fprint(os.Stderr, usage)
	return fmt.Errorf("bad arguments for %s", cmd)
}

func printToken(store *app.Store, p *app.Principal) error {
	token, _, err := store.IssueToken(p, "cli")
	if err != nil {
		return err
	}
	fmt.Printf("%s\t%s\n", p.Name, token)
	return nil
}
