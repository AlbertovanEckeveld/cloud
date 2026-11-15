package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/AlbertovanEckeveld/cloud/internal/identity"
)

const userUsage = `usage:
  platform user create -username NAME -name "Display Name" [-email ADDRESS] [-admin]
  platform user reset  -username NAME

Both print a one-time activation link (valid 72 hours). With it the user sets
a password and links an authenticator app. "reset" first revokes the current
password, second factor and sessions: use it when a user is locked out.`

// userCommand implements the "user" subcommands. It needs shell access to
// the server (or "docker exec"), which is exactly the point: nobody can claim
// the first admin account over the network.
func userCommand(ctx context.Context, out io.Writer, args []string) error {
	if len(args) == 0 {
		return errors.New(userUsage)
	}
	fs := flag.NewFlagSet("user "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	username := fs.String("username", "", "login name")
	displayName := fs.String("name", "", "display name")
	email := fs.String("email", "", "e-mail address (optional; the link is also mailed there)")
	admin := fs.Bool("admin", false, "grant administrator rights")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 || *username == "" {
		return errors.New(userUsage)
	}

	// Logs go to stderr so stdout carries only the result.
	a, err := setup(ctx, os.Stderr)
	if err != nil {
		return err
	}
	defer a.pool.Close()
	client := identity.Client{IP: "", UserAgent: "cli"}

	switch args[0] {
	case "create":
		if *displayName == "" {
			return errors.New(userUsage)
		}
		u, err := a.identity.CreateUser(ctx, client, identity.NewUser{Username: *username, DisplayName: *displayName, IsAdmin: *admin, Email: *email})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Created user %q.\n", u.Username)
	case "reset":
		if *displayName != "" || *admin || *email != "" {
			return errors.New(userUsage)
		}
	default:
		return errors.New(userUsage)
	}

	activation, err := a.identity.IssueActivation(ctx, client, *username)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Activation link (valid 72 hours, single use):\n\n  %s\n\nAnyone with this link can take over the account: share it only over a private channel.\n", activation.Link)
	if activation.Emailed {
		fmt.Fprintln(out, "The link has also been queued for sending to the user's e-mail address.")
	}
	return nil
}
