package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/paulwritescode/lighthouse/internal/dock"
	"github.com/paulwritescode/lighthouse/internal/store"
)

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Println(string(b))
	return err
}

// parseDuration accepts Go durations plus a "d" suffix for days.
func parseDuration(v string) (time.Duration, error) {
	if v == "" || v == "0" || v == "never" {
		return 0, nil
	}
	if strings.HasSuffix(v, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(v, "d"))
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q", v)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(v)
}

func queryCommand(s *store.Store, ctx context.Context, args []string) error {
	f := flag.NewFlagSet("query", flag.ContinueOnError)
	kind := f.String("kind", "", "record kind or glob such as dock_*")
	project := f.String("project", "", "project scope")
	where := f.String("where", "", "where document as JSON")
	order := f.String("order", "", "comma-separated paths, each optionally path:asc or path:desc")
	sel := f.String("select", "", "comma-separated paths to return")
	limit := f.Int("limit", 50, "page size, 1 to 1000")
	cursor := f.String("cursor", "", "cursor from a previous page")
	deleted := f.Bool("include-deleted", false, "include soft-deleted records")
	count := f.Bool("count", false, "print only the number of matches")
	format := f.String("format", "json", "json or table")
	if err := f.Parse(args); err != nil {
		return err
	}
	spec := store.QuerySpec{Kind: *kind, Project: *project, Limit: *limit, Cursor: *cursor, IncludeDeleted: *deleted, Count: *count}
	if *where != "" {
		if err := json.Unmarshal([]byte(*where), &spec.Where); err != nil {
			return fmt.Errorf("--where is not valid JSON: %w", err)
		}
	}
	for _, part := range strings.Split(*order, ",") {
		if part = strings.TrimSpace(part); part != "" {
			p, d, _ := strings.Cut(part, ":")
			spec.Order = append(spec.Order, store.OrderKey{Path: p, Dir: d})
		}
	}
	for _, part := range strings.Split(*sel, ",") {
		if part = strings.TrimSpace(part); part != "" {
			spec.Select = append(spec.Select, part)
		}
	}
	res, err := s.Query(ctx, spec)
	if err != nil {
		return err
	}
	if *format == "table" && !*count {
		for _, it := range res.Items {
			fmt.Printf("%v\t%v\t%v\t%v\n", it["id"], it["kind"], it["updated_at"], it["title"])
		}
		if res.NextCursor != "" {
			fmt.Fprintln(os.Stderr, "more results: --cursor", res.NextCursor)
		}
		return nil
	}
	return printJSON(res)
}

func changesCommand(s *store.Store, ctx context.Context, args []string) error {
	f := flag.NewFlagSet("changes", flag.ContinueOnError)
	since := f.Int64("since", 0, "show changes after this sequence number")
	limit := f.Int("limit", 500, "page size")
	kinds := f.String("kinds", "", "comma-separated kinds or globs")
	follow := f.Bool("follow", false, "keep running and print new changes as newline-delimited JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	var kindList []string
	if *kinds != "" {
		kindList = strings.Split(*kinds, ",")
	}
	enc := json.NewEncoder(os.Stdout)
	pos := *since
	for {
		page, err := s.Changes(ctx, store.ChangeQuery{Since: pos, Limit: *limit, Kinds: kindList})
		if err != nil {
			return err
		}
		if !*follow {
			return printJSON(page)
		}
		if page.Reset {
			return errors.New("the position is ahead of this database's change feed; restart from --since 0")
		}
		for _, c := range page.Items {
			if err := enc.Encode(c); err != nil {
				return err
			}
		}
		pos = page.NextSeq
		if len(page.Items) < *limit {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
}

func tokenCommand(s *store.Store, ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: lighthouse token create|list|revoke|rotate")
	}
	switch args[0] {
	case "create":
		f := flag.NewFlagSet("token create", flag.ContinueOnError)
		name := f.String("name", "", "token name, for example dock")
		var scopes listFlag
		f.Var(&scopes, "scope", "scope such as read:* or write:dock_* (repeatable)")
		expires := f.String("expires", "", "lifetime such as 90d or 8760h (default: never)")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		ttl, err := parseDuration(*expires)
		if err != nil {
			return err
		}
		secret, tok, err := s.CreateToken(ctx, *name, scopes, ttl)
		if err != nil {
			return err
		}
		fmt.Printf("Token %q created with scopes %s.\nCopy it now. It cannot be shown again.\n\n%s\n", tok.Name, strings.Join(tok.Scopes, ", "), secret)
		return nil
	case "list":
		tokens, err := s.ListTokens(ctx)
		if err != nil {
			return err
		}
		for _, t := range tokens {
			state := "active"
			switch {
			case t.RevokedAt != nil:
				state = "revoked"
			case t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt):
				state = "expired"
			}
			last := "never"
			if t.LastUsedAt != nil {
				last = t.LastUsedAt.Local().Format("2006-01-02 15:04")
			}
			fmt.Printf("%s\t%s\t%s\t%s\tlast used %s\n", t.ID, t.Name, state, strings.Join(t.Scopes, ","), last)
		}
		return nil
	case "revoke":
		if len(args) != 2 {
			return errors.New("usage: lighthouse token revoke <name|id>")
		}
		return s.RevokeToken(ctx, args[1])
	case "rotate":
		if len(args) != 2 {
			return errors.New("usage: lighthouse token rotate <name|id>")
		}
		secret, tok, err := s.RotateToken(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Token %q rotated. The old secret no longer works. Copy the new one now:\n\n%s\n", tok.Name, secret)
		return nil
	}
	return fmt.Errorf("unknown token command %q", args[0])
}

func kindCommand(s *store.Store, ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: lighthouse kind install dock | list | show <kind> | export dock")
	}
	switch args[0] {
	case "install":
		if len(args) != 2 || args[1] != "dock" {
			return errors.New("usage: lighthouse kind install dock")
		}
		specs, err := dock.Install(ctx, s)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(specs))
		for _, sp := range specs {
			names = append(names, sp.Kind)
		}
		fmt.Printf("Registered %d Dock kinds: %s\n", len(names), strings.Join(names, ", "))
		return nil
	case "export":
		return printJSON(dock.Kinds())
	case "list":
		kinds, err := s.ListKinds(ctx)
		if err != nil {
			return err
		}
		sort.Slice(kinds, func(i, j int) bool { return kinds[i]["kind"].(string) < kinds[j]["kind"].(string) })
		for _, k := range kinds {
			fmt.Printf("%v\tv%v\t%v records\tvalidation %v\n", k["kind"], k["version"], k["count"], k["validation"])
		}
		return nil
	case "show":
		if len(args) != 2 {
			return errors.New("usage: lighthouse kind show <kind>")
		}
		spec, err := s.GetKind(ctx, args[1])
		if err != nil {
			return err
		}
		if spec == nil {
			return fmt.Errorf("kind %q is not registered", args[1])
		}
		return printJSON(spec)
	}
	return fmt.Errorf("unknown kind command %q", args[0])
}

func doctorCommand(s *store.Store, ctx context.Context, path string) error {
	failed := false
	for _, c := range s.Doctor(ctx, path) {
		mark := map[string]string{"ok": "ok  ", "warn": "warn", "fail": "FAIL"}[c.Status]
		fmt.Printf("%s  %-14s %s\n", mark, c.Name, c.Detail)
		failed = failed || c.Status == "fail"
	}
	if failed {
		return errors.New("doctor found a problem")
	}
	return nil
}

func backupCommand(s *store.Store, ctx context.Context, path string, args []string) error {
	f := flag.NewFlagSet("backup", flag.ContinueOnError)
	dir := f.String("output", "", "folder for the backup (default: backups next to the database)")
	keep := f.Int("keep", 14, "number of newest backups to keep")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		*dir = filepath.Join(filepath.Dir(path), "backups")
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	target := filepath.Join(*dir, "lighthouse-"+time.Now().UTC().Format("20060102T150405Z")+".db")
	if err := s.BackupTo(ctx, target); err != nil {
		return err
	}
	_ = os.Chmod(target, 0o600)
	if _, err := s.PruneHistory(ctx); err != nil {
		return err
	}
	matches, _ := filepath.Glob(filepath.Join(*dir, "lighthouse-*.db"))
	sort.Strings(matches)
	for len(matches) > *keep && *keep > 0 {
		_ = os.Remove(matches[0])
		matches = matches[1:]
	}
	fmt.Println(target)
	return nil
}
