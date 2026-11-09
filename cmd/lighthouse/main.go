package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/paulwritescode/lighthouse"
	"github.com/paulwritescode/lighthouse/internal/codecall"
	"github.com/paulwritescode/lighthouse/internal/hostname"
	"github.com/paulwritescode/lighthouse/internal/service"
	"github.com/paulwritescode/lighthouse/internal/store"
	"github.com/paulwritescode/lighthouse/internal/transfer"
	"github.com/paulwritescode/lighthouse/internal/tui"
	"github.com/paulwritescode/lighthouse/internal/updater"
	"github.com/paulwritescode/lighthouse/internal/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "lighthouse:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	defaultLaunch := len(args) == 0
	if len(args) == 0 {
		args = []string{"menu"}
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
		var err error
		path, err = lighthouse.DefaultDatabasePath()
		if err != nil {
			return fmt.Errorf("choose database path: %w", err)
		}
	}
	if command == "version" {
		fmt.Printf("Lighthouse %s\n", updater.CurrentVersion())
		return nil
	}
	if command == "update" || command == "self-update" {
		return updater.Update(context.Background(), path)
	}
	if command == "menu" {
		if defaultLaunch {
			complete, err := setupIsComplete()
			if err != nil {
				return err
			}
			if !complete {
				return firstRunSetup(path)
			}
		}
		return managementMenu(path)
	}
	if command == "setup" {
		return firstRunSetup(path)
	}
	if command == "service" {
		return serviceCommand(path, commandArgs)
	}
	if command == "host" {
		return hostCommand(commandArgs)
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
	case "update", "self-update":
		return updater.Update(ctx, path)
	default:
		return fmt.Errorf("unknown command %q\n%s", command, usageText)
	}
}

const usageText = "Commands: setup, menu, init, serve, service, host, add, search, context, code, export, import, version, update\nUse lighthouse <command> --help for command options. Running `lighthouse` opens first-time setup or the interactive menu."

func usage() error { fmt.Println("Lighthouse — local agent knowledge\n\n" + usageText); return nil }

func managementMenu(dbPath string) error {
	manager, err := service.New("", dbPath)
	if err != nil {
		return err
	}
	hostRegistered, _ := hostname.Registered()
	action, err := tui.Run(updater.CurrentVersion(), manager.Installed(), hostRegistered)
	if err != nil {
		return err
	}
	switch action {
	case tui.Exit:
		return nil
	case tui.StartDashboard:
		return run([]string{"serve", "--db", dbPath})
	case tui.StartService:
		return runServiceAction(dbPath, "start")
	case tui.StopDashboard:
		return runServiceAction(dbPath, "stop")
	case tui.RestartDashboard:
		return runServiceAction(dbPath, "restart")
	case tui.ForceStopDashboard:
		return runServiceAction(dbPath, "force-stop")
	case tui.InstallStartup:
		return runServiceAction(dbPath, "install")
	case tui.RemoveStartup:
		return runServiceAction(dbPath, "remove")
	case tui.SetupLocalName:
		return hostCommand([]string{"register"})
	case tui.RemoveLocalName:
		return hostCommand([]string{"remove"})
	case tui.CheckUpdates:
		release, err := updater.Latest(context.Background())
		if err != nil {
			return err
		}
		current := updater.CurrentVersion()
		if current == "development build" {
			fmt.Printf("Latest release: %s (%s)\nInstall it with: lighthouse update\n", release.TagName, release.HTMLURL)
		} else if updater.IsNewer(current, release.TagName) {
			fmt.Printf("Update available: %s → %s\n%s\nRun `lighthouse update` to install it.\n", current, release.TagName, release.HTMLURL)
		} else {
			fmt.Printf("Lighthouse %s is up to date.\n", current)
		}
		return nil
	case tui.Update:
		fmt.Printf("This installs the latest published version into Go's binary directory. Continue? [y/N] ")
		answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && err.Error() != "EOF" {
			return err
		}
		if strings.ToLower(strings.TrimSpace(answer)) != "y" && strings.ToLower(strings.TrimSpace(answer)) != "yes" {
			fmt.Println("Update cancelled.")
			return nil
		}
		return updater.Update(context.Background(), dbPath)
	case tui.OpenDashboard:
		return openDashboard()
	default:
		return fmt.Errorf("service action %q is not yet available for this operating system", action)
	}
}

func firstRunSetup(dbPath string) error {
	options, finished, err := tui.RunSetup()
	if err != nil {
		return err
	}
	if !finished {
		fmt.Println("Setup was not completed. Run `lighthouse setup` when you are ready.")
		return nil
	}

	database, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("initialize the local database: %w", err)
	}
	if err := database.Close(); err != nil {
		return fmt.Errorf("close the local database after setup: %w", err)
	}

	if options.RegisterHostname {
		if err := hostname.Register(); err != nil {
			if errors.Is(err, os.ErrPermission) || strings.Contains(err.Error(), "administrator permission") {
				if err := hostname.Elevate(false); err != nil {
					return fmt.Errorf("register the Lighthouse hostname: %w", err)
				}
			} else {
				return fmt.Errorf("register the Lighthouse hostname: %w", err)
			}
		}
	}

	if options.InstallService {
		manager, err := service.New("", dbPath)
		if err != nil {
			return err
		}
		if err := manager.Install(); err != nil {
			return fmt.Errorf("install the startup service: %w", err)
		}
	}

	if err := markSetupComplete(); err != nil {
		return err
	}
	fmt.Println("Setup complete. Opening the Lighthouse menu…")
	return managementMenu(dbPath)
}

func setupStatePath() (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find Lighthouse configuration directory: %w", err)
	}
	return filepath.Join(config, "lighthouse", "setup-complete"), nil
}

func setupIsComplete() (bool, error) {
	path, err := setupStatePath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("check Lighthouse setup status: %w", err)
}

func markSetupComplete() error {
	path, err := setupStatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create Lighthouse configuration directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(updater.CurrentVersion()+"\n"), 0o600); err != nil {
		return fmt.Errorf("save Lighthouse setup status: %w", err)
	}
	return nil
}

func runServiceAction(dbPath, action string) error {
	switch action {
	case "install":
		if !confirm("Install and start the Lighthouse user service at sign-in? [y/N] ") {
			fmt.Println("Startup service setup cancelled.")
			return nil
		}
	case "remove":
		if !confirm("Stop Lighthouse and remove its startup service? [y/N] ") {
			fmt.Println("Startup service removal cancelled.")
			return nil
		}
	case "force-stop":
		if !confirm("Force stop the Lighthouse dashboard service? [y/N] ") {
			fmt.Println("Force stop cancelled.")
			return nil
		}
	}
	manager, err := service.New("", dbPath)
	if err != nil {
		return err
	}
	if action != "install" && action != "remove" && !manager.Installed() {
		return errors.New("no Lighthouse startup service is installed; choose Install startup service first")
	}
	switch action {
	case "install":
		err = manager.Install()
	case "remove":
		err = manager.Remove()
	case "start":
		err = manager.Start()
	case "stop":
		err = manager.Stop()
	case "restart":
		err = manager.Restart()
	case "force-stop":
		err = manager.ForceStop()
	case "status":
		if manager.Installed() {
			fmt.Println("Lighthouse startup service is installed.")
		} else {
			fmt.Println("Lighthouse startup service is not installed.")
		}
		return nil
	default:
		return fmt.Errorf("unknown service action %q; use install, remove, start, stop, restart, force-stop, or status", action)
	}
	if err != nil {
		return fmt.Errorf("service %s: %w", action, err)
	}
	fmt.Printf("Lighthouse service %s complete.\n", action)
	return nil
}

func serviceCommand(dbPath string, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Println("Usage: lighthouse service install|remove|start|stop|restart|force-stop|status")
		return nil
	}
	return runServiceAction(dbPath, args[0])
}

func hostCommand(args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Println("Usage: lighthouse host register|remove|status")
		return nil
	}
	switch args[0] {
	case "register":
		if !confirm("Add lighthouse to the local hosts file? This may request administrator permission. [y/N] ") {
			fmt.Println("Hostname setup cancelled.")
			return nil
		}
		if err := hostname.Register(); err != nil {
			if errors.Is(err, os.ErrPermission) || strings.Contains(err.Error(), "administrator permission") {
				return hostname.Elevate(false)
			}
			return err
		}
		fmt.Println("Open Lighthouse at http://lighthouse:8080")
		return nil
	case "remove":
		if !confirm("Remove Lighthouse's managed entries from the local hosts file? [y/N] ") {
			fmt.Println("Hostname removal cancelled.")
			return nil
		}
		if err := hostname.Remove(); err != nil {
			if errors.Is(err, os.ErrPermission) || strings.Contains(err.Error(), "administrator permission") {
				return hostname.Elevate(true)
			}
			return err
		}
		fmt.Println("Lighthouse hostname entries removed.")
		return nil
	case "status":
		registered, err := hostname.Registered()
		if err != nil {
			return err
		}
		if registered {
			fmt.Println("http://lighthouse:8080 is registered in the local hosts file.")
		} else {
			fmt.Println("The local lighthouse hostname is not registered; use http://127.0.0.1:8080.")
		}
		return nil
	case "apply":
		return hostname.ApplyRegistered()
	case "remove-apply":
		return hostname.ApplyRemoved()
	default:
		return fmt.Errorf("unknown host action %q; use register, remove, or status", args[0])
	}
}

func confirm(prompt string) bool {
	fmt.Print(prompt)
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes")
}

func openDashboard() error {
	url := "http://127.0.0.1:8080"
	if registered, err := hostname.Registered(); err == nil && registered {
		url = "http://lighthouse:8080"
	}
	client := &http.Client{Timeout: 900 * time.Millisecond}
	response, err := client.Get(url + "/healthz")
	if err != nil {
		return fmt.Errorf("dashboard is not responding at %s; start it with `lighthouse serve`", url)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("dashboard returned %s", response.Status)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open %s in your browser: %w", url, err)
	}
	return nil
}

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
	shutdownSignal, stop := signal.NotifyContext(context.Background(), terminationSignals()...)
	defer stop()
	go func() {
		<-shutdownSignal.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	err = srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
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
