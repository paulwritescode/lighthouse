package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/paulkinyatti/lighthouse/internal/codecall"
	"github.com/paulkinyatti/lighthouse/internal/store"
	"github.com/paulkinyatti/lighthouse/internal/transfer"
	"github.com/paulkinyatti/lighthouse/internal/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "lighthouse:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		return usage()
	}
	command := args[0]
	var path string
	commandArgs := make([]string, 0, len(args)-1)
	for i := 1; i < len(args); i++ {
		if args[i] == "--db" {
			if i+1 >= len(args) {
				return errors.New("--db requires a path")
			}
			i++
			path = args[i]
			continue
		}
		if strings.HasPrefix(args[i], "--db=") {
			path = strings.TrimPrefix(args[i], "--db=")
			continue
		}
		commandArgs = append(commandArgs, args[i])
	}
	if path == "" {
		path = os.Getenv("LIGHTHOUSE_DB")
	}
	if path == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		path = filepath.Join(base, "lighthouse", "lighthouse.db")
	}
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()
	switch command {
	case "init":
		fmt.Println(path)
		return nil
	case "serve":
		return serve(s, path, commandArgs)
	case "add":
		return add(s, ctx, commandArgs)
	case "search":
		return search(s, ctx, commandArgs)
	case "context":
		return loadContext(s, ctx, commandArgs)
	case "code":
		return runCode(s, ctx, commandArgs)
	case "export":
		return exportData(s, ctx, commandArgs)
	case "import":
		return importData(s, ctx, commandArgs)
	default:
		return fmt.Errorf("unknown command %q\n%s", command, usageText)
	}
}

const usageText = "Commands: init, serve, add, search, context, code, export, import\nUse lighthouse <command> --help for command options."

func usage() error { fmt.Println("Lighthouse — local agent knowledge\n\n" + usageText); return nil }
func serve(s *store.Store, path string, args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	host := f.String("host", "127.0.0.1", "loopback interface")
	port := f.Int("port", 8080, "HTTP port")
	if err := f.Parse(args); err != nil {
		return err
	}
	ip := net.ParseIP(*host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("serve only accepts a loopback IP address")
	}
	if *port < 1 || *port > 65535 {
		return errors.New("port must be from 1 to 65535")
	}
	handler, err := web.New(s, path)
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("%s:%d", *host, *port)
	srv := &http.Server{Addr: addr, Handler: handler.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	friendly := "http://lighthouse.localhost:" + fmt.Sprint(*port)
	fmt.Printf("Lighthouse listening on http://%s (friendly address: %s)\nDatabase: %s\nPress Ctrl+C to stop.\n", addr, friendly, path)
	return srv.ListenAndServe()
}
func add(s *store.Store, ctx context.Context, args []string) error {
	f := flag.NewFlagSet("add", flag.ContinueOnError)
	title := f.String("title", "", "result title")
	project := f.String("project", "", "project scope")
	agent := f.String("agent", "agent", "agent name")
	kind := f.String("kind", "result", "result kind")
	if err := f.Parse(args); err != nil {
		return err
	}
	content, err := io.ReadAll(io.LimitReader(os.Stdin, (2<<20)+1))
	if err != nil {
		return err
	}
	if len(content) > 2<<20 {
		return errors.New("stdin exceeds 2 MiB")
	}
	a, err := s.SaveArtifact(ctx, *title, string(content), *project, *agent, *kind)
	if err != nil {
		return err
	}
	return writeJSON(a)
}
func search(s *store.Store, ctx context.Context, args []string) error {
	f := flag.NewFlagSet("search", flag.ContinueOnError)
	q := f.String("query", "", "full-text query")
	project := f.String("project", "", "project scope")
	limit := f.Int("limit", 10, "maximum results")
	if err := f.Parse(args); err != nil {
		return err
	}
	items, err := s.Search(ctx, *q, *project, *limit)
	if err != nil {
		return err
	}
	return writeJSON(items)
}
func loadContext(s *store.Store, ctx context.Context, args []string) error {
	f := flag.NewFlagSet("context", flag.ContinueOnError)
	project := f.String("project", "", "project scope")
	session := f.String("session", "", "conversation identifier")
	limit := f.Int("limit", 10, "items per source type")
	if err := f.Parse(args); err != nil {
		return err
	}
	p, err := s.LoadContext(ctx, *session, *project, *limit)
	if err != nil {
		return err
	}
	return writeJSON(p)
}
func runCode(s *store.Store, ctx context.Context, args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Println("Usage: lighthouse code [--project NAME] [--session ID] < script.star\n\nRuns one bounded Starlark code call. Available functions:\n  load_memory(project=\"\", session=\"\", limit=10)\n  search(query, project=\"\", limit=10)\n  get(id)\n  describe(project=\"\")\n  audit(limit=25)\n  save(title, content, project=\"\", agent=\"agent\", kind=\"result\", fields={})\n  update(id, fields={...})\n  delete(id)\n  upsert_memory(key, body, project=\"\", kind=\"fact\", source_id=\"\")\n  delete_memory(id, project=\"\")\n  emit(value)\n\nWrites return the committed item and a refreshed context patch. Custom fields are JSON document properties; SQL schema changes are not available.")
		return nil
	}
	f := flag.NewFlagSet("code", flag.ContinueOnError)
	project := f.String("project", "", "default project scope")
	session := f.String("session", "", "conversation identifier")
	if err := f.Parse(args); err != nil {
		return err
	}
	src, err := io.ReadAll(io.LimitReader(os.Stdin, (64<<10)+1))
	if err != nil {
		return err
	}
	if len(src) > 64<<10 {
		return errors.New("stdin exceeds 64 KiB")
	}
	return (codecall.Runner{Store: s}).Run(ctx, string(src), *session, *project, os.Stdout)
}

func exportData(s *store.Store, ctx context.Context, args []string) error {
	f := flag.NewFlagSet("export", flag.ContinueOnError)
	format := f.String("format", "json", "json, md, yaml, toml, pdf, or db")
	output := f.String("output", "", "write to a file instead of stdout")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *format == "db" {
		tmp, err := os.CreateTemp("", "lighthouse-export-*.db")
		if err != nil {
			return err
		}
		path := tmp.Name()
		tmp.Close()
		os.Remove(path)
		defer os.Remove(path)
		if err = s.BackupTo(ctx, path); err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		var dst io.Writer = os.Stdout
		if *output != "" {
			out, err := os.OpenFile(*output, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			defer out.Close()
			dst = out
		}
		_, err = io.Copy(dst, src)
		return err
	}
	var data []byte
	var contentType string
	{
		a, err := s.AllArtifacts(ctx)
		if err != nil {
			return err
		}
		m, err := s.AllMemory(ctx)
		if err != nil {
			return err
		}
		data, contentType, _, err = transfer.Export(*format, a, m)
		if err != nil {
			return err
		}
	}
	if *output != "" {
		return os.WriteFile(*output, data, 0o600)
	}
	if contentType == "application/pdf" || contentType == "application/vnd.sqlite3" {
		_, err := os.Stdout.Write(data)
		return err
	}
	_, err := os.Stdout.Write(append(data, '\n'))
	return err
}

func importData(s *store.Store, ctx context.Context, args []string) error {
	f := flag.NewFlagSet("import", flag.ContinueOnError)
	format := f.String("format", "json", "json, md, yaml, toml, pdf, or db")
	file := f.String("file", "", "read from a file instead of stdin")
	if err := f.Parse(args); err != nil {
		return err
	}
	*format = strings.ToLower(*format)
	var raw []byte
	var err error
	var dataset transfer.Dataset
	if *format == "db" {
		path := *file
		if path == "" {
			tmp, e := os.CreateTemp("", "lighthouse-import-*.db")
			if e != nil {
				return e
			}
			path = tmp.Name()
			defer os.Remove(path)
			n, e := io.Copy(tmp, io.LimitReader(os.Stdin, (2<<30)+1))
			if e != nil {
				tmp.Close()
				return e
			}
			if n > 2<<30 {
				tmp.Close()
				return errors.New("database import exceeds the 2 GiB limit")
			}
			if e = tmp.Close(); e != nil {
				return e
			}
		} else if info, statErr := os.Stat(path); statErr != nil {
			return statErr
		} else if info.Size() > 2<<30 {
			return errors.New("database import exceeds the 2 GiB limit")
		}
		dataset.Artifacts, dataset.Memories, err = store.ReadDatabase(ctx, path)
		if err != nil {
			return err
		}
	} else if *file != "" {
		limit := int64(16 << 20)
		if *format == "pdf" {
			limit = 1_400 << 10
		}
		input, openErr := os.Open(*file)
		if openErr != nil {
			return openErr
		}
		raw, err = io.ReadAll(io.LimitReader(input, limit+1))
		input.Close()
		if int64(len(raw)) > limit {
			return errors.New("import exceeds the size limit for this format")
		}
	} else {
		limit := int64(16 << 20)
		if *format == "pdf" {
			limit = 1_400 << 10
		}
		raw, err = io.ReadAll(io.LimitReader(os.Stdin, limit+1))
		if int64(len(raw)) > limit {
			return errors.New("import exceeds the size limit for this format")
		}
	}
	if err != nil {
		return err
	}
	if len(raw) > 16<<20 {
		return errors.New("import exceeds the 16 MiB input limit")
	}
	if *format != "db" {
		dataset, err = transfer.Import(*format, raw)
		if err != nil {
			return err
		}
	}
	a, err := s.ImportArtifacts(ctx, dataset.Artifacts)
	if err != nil {
		return err
	}
	m, err := s.ImportMemories(ctx, dataset.Memories)
	if err != nil {
		return err
	}
	return writeJSON(map[string]any{"imported_artifacts": a, "imported_memories": m, "skipped_duplicates": len(dataset.Artifacts) + len(dataset.Memories) - a - m})
}
func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
