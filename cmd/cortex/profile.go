package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/sausheong/cortex"
)

func cmdProfile() {
	track := ""
	untrack := ""
	jsonOut := false
	target := "" // entity id; empty = owner
	usage := "usage: cortex profile [<entity-id>] [--track <id>] [--untrack <id>] [--json]\n" +
		"  (no args)        show the owner's profile\n" +
		"  <entity-id>      show a specific entity's profile\n" +
		"  --track <id>     mark an entity for auto-refresh during maintain\n" +
		"  --untrack <id>   stop auto-refresh and drop its cached profile\n" +
		"  --json           print the profile as JSON"
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--track":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			i++
			track = args[i]
		case "--untrack":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			i++
			untrack = args[i]
		case "--json":
			jsonOut = true
		default:
			if len(args[i]) > 0 && args[i][0] == '-' {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			target = args[i]
		}
	}

	cx := openCortex()
	defer cx.Close()
	ctx := context.Background()

	if track != "" {
		if err := cx.TrackProfile(ctx, track); err != nil {
			fmt.Fprintf(os.Stderr, "track: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("tracking profile for %s\n", track)
		return
	}
	if untrack != "" {
		if err := cx.UntrackProfile(ctx, untrack); err != nil {
			fmt.Fprintf(os.Stderr, "untrack: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("untracked profile for %s\n", untrack)
		return
	}

	// Resolve target: explicit id, else owner.
	entityID := target
	isOwnerTarget := false
	if entityID == "" {
		owners, err := cx.FindEntities(ctx, cortex.EntityFilter{Type: "person", Source: "owner"})
		if err != nil {
			fmt.Fprintf(os.Stderr, "find owner: %v\n", err)
			os.Exit(1)
		}
		if len(owners) == 0 {
			fmt.Fprintln(os.Stderr, "no owner configured. Run 'cortex init' or 'cortex config --name <name>'.")
			os.Exit(1)
		}
		entityID = owners[0].ID
		isOwnerTarget = true
	}

	p, err := cx.Profile(ctx, entityID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "profile: %v\n", err)
		os.Exit(1)
	}

	if jsonOut {
		data, mErr := json.MarshalIndent(p, "", "  ")
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "encode profile: %v\n", mErr)
			os.Exit(1)
		}
		fmt.Println(string(data))
		return
	}

	fmt.Print(cortex.RenderProfileMarkdown(p))

	// Tip for ad-hoc (non-owner) profiles that aren't tracked.
	if !isOwnerTarget {
		fmt.Printf("\ntip: cortex profile --track %s to keep it fresh during maintain.\n", entityID)
	}
}
