package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"

	"github.com/paulwritescode/lighthouse/internal/service"
)

const releaseURL = "https://api.github.com/repos/paulwritescode/lighthouse/releases/latest"
const modulePath = "github.com/paulwritescode/lighthouse/cmd/lighthouse@latest"

type Release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

func CurrentVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "development build"
}

func Latest(ctx context.Context) (Release, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return Release{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "Lighthouse-CLI")
	client := &http.Client{Timeout: 8 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return Release{}, fmt.Errorf("check GitHub releases: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("GitHub release check returned %s", response.Status)
	}
	var release Release
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return Release{}, err
	}
	if release.TagName == "" {
		return Release{}, errors.New("GitHub returned an empty release tag")
	}
	return release, nil
}

func Update(ctx context.Context, database string) error {
	goPath, err := exec.LookPath("go")
	if err != nil {
		return errors.New("Go is required for self-update; install Go and retry, or reinstall from the installation guide")
	}
	manager, err := service.New("", database)
	if err != nil {
		return err
	}
	restartService := manager.Installed()
	if restartService {
		if err := manager.Stop(); err != nil {
			return fmt.Errorf("stop Lighthouse service before updating: %w", err)
		}
	}
	cmd := exec.CommandContext(ctx, goPath, "install", modulePath)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Stdin = os.Stdin
	installErr := cmd.Run()
	if restartService {
		if err := manager.Start(); err != nil {
			if installErr != nil {
				return errors.Join(fmt.Errorf("install latest Lighthouse: %w", installErr), fmt.Errorf("restart previous Lighthouse service: %w", err))
			}
			return fmt.Errorf("updated Lighthouse, but could not restart its service: %w", err)
		}
		fmt.Println("Lighthouse service restarted with the installed version.")
	}
	if installErr != nil {
		return fmt.Errorf("install latest Lighthouse: %w", installErr)
	}
	return nil
}

func IsNewer(current, latest string) bool {
	current = strings.TrimPrefix(strings.TrimSpace(current), "v")
	latest = strings.TrimPrefix(strings.TrimSpace(latest), "v")
	if current == "" || current == "development build" || current == latest {
		return false
	}
	var a, b [3]int
	if _, err := fmt.Sscanf(current, "%d.%d.%d", &a[0], &a[1], &a[2]); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(latest, "%d.%d.%d", &b[0], &b[1], &b[2]); err != nil {
		return false
	}
	for i := range a {
		if b[i] != a[i] {
			return b[i] > a[i]
		}
	}
	return false
}
