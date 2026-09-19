package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/rowsetdev/rowset-studio/rowset-core/internal/config"
	"github.com/rowsetdev/rowset-studio/rowset-core/internal/store"
)

type desktopState struct {
	Port int    `json:"port"`
	Key  string `json:"key"`
}

// desktopDataDir is where instance.json, the database, snapshots and logs
// for this desktop installation live.
func desktopDataDir() (string, error) {
	if directory := os.Getenv("ROWSET_DESKTOP_DIR"); directory != "" {
		return directory, nil
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Rowset", "Community"), nil
}

func desktop(command Context) error {
	directory, err := desktopDataDir()
	if err != nil {
		return err
	}
	// A plain `rowset` / `rowset desktop` typed into a terminal, or run from
	// a Windows shortcut, should keep running after that terminal closes or
	// the shortcut's own launcher process exits - the same way double-
	// clicking the macOS menu-bar app does. Re-exec detached once, then let
	// the detached copy (ROWSET_DETACHED=1) do the actual work below. The
	// macOS app sets ROWSET_DETACHED=1 itself when it spawns this, since it
	// already manages the child's lifecycle directly (it needs the process
	// handle to notice a later crash, not just a failed launch).
	if os.Getenv("ROWSET_DETACHED") != "1" {
		return spawnDetached(directory)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	unlock, err := lockDesktop(filepath.Join(directory, "instance.lock"))
	if err != nil {
		// An existing process may still be initializing its listener.
		for i := 0; i < 30; i++ {
			if err := openDesktopState(directory); err == nil {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return errors.New("Rowset is already starting or running; try opening it again shortly")
	}
	defer unlock()
	statePath := filepath.Join(directory, "instance.json")
	var previous desktopState
	if raw, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(raw, &previous)
	}
	port := previous.Port
	if port < 1024 || port > 65535 {
		port = 18765
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		listener, err = net.Listen("tcp4", "127.0.0.1:0")
	}
	if err != nil {
		return err
	}
	defer listener.Close()
	port = listener.Addr().(*net.TCPAddr).Port
	configPath := filepath.Join(directory, "rowset-community.env")
	// Starting on an empty directory creates a new installation with no
	// connections. That is right the first time and alarming every other time,
	// so say which directory is in use and never do it silently.
	if _, err := os.Stat(filepath.Join(directory, "rowset-community.sqlite3")); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "Rowset: no database in %s — starting a new installation there.\n", directory)
		fmt.Fprintf(os.Stderr, "Rowset: if you expected your existing connections, stop Rowset and start it with ROWSET_DESKTOP_DIR set to your data directory.\n")
	} else {
		fmt.Fprintf(os.Stderr, "Rowset: data directory %s\n", directory)
	}
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		jwt, err := randomHex(32)
		if err != nil {
			return err
		}
		enc, err := randomBase64(32)
		if err != nil {
			return err
		}
		if err := writePrivateConfig(configPath, []string{"ROWSET_ENV=production", "ROWSET_BIND_HOST=127.0.0.1", "ROWSET_SECURE_COOKIES=false", "ROWSET_JWT_SECRET=" + jwt, "ROWSET_ENC_KEY=" + enc, "ROWSET_DB_PATH=" + filepath.Join(directory, "rowset-community.sqlite3")}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	// Explicit desktop values override ambient deployment settings.
	for key, value := range readEnvValues(configPath) {
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	if err := os.Setenv("ROWSET_CONFIG", configPath); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cfg.APIPort = uint16(port)
	cfg.BindHost = "127.0.0.1"
	cfg.SecureCookies = false
	cfg.LocalOwnerEmail = "local@rowset.studio"
	cfg.LocalLauncherKey, err = randomHex(32)
	if err != nil {
		return err
	}
	snapshots := filepath.Join(directory, "snapshots")
	data, err := store.Open(context.Background(), cfg.DBPath, store.WithMigrationBackup(snapshots))
	if err != nil {
		return err
	}
	defer data.Close()
	if err := data.ClaimMode(context.Background(), false); err != nil {
		return err
	}
	hasUsers, err := data.HasUsers(context.Background())
	if err != nil {
		return err
	}
	if !hasUsers {
		password, err := randomHex(32)
		if err != nil {
			return err
		}
		if err := createInitialAdmin(context.Background(), data, cfg.LocalOwnerEmail, password); err != nil {
			return err
		}
	}
	if _, err := data.UserByEmail(context.Background(), cfg.LocalOwnerEmail); err != nil {
		return errors.New("this desktop data directory does not contain a desktop owner")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg.LocalShutdown = stop
	app, err := command.NewServer(ServerOptions{Config: cfg, Store: data, Logger: buildLogger(filepath.Join(directory, "logs"))})
	if err != nil {
		return err
	}
	defer app.Close()
	server := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	state := desktopState{Port: port, Key: cfg.LocalLauncherKey}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.WriteFile(statePath, raw, 0600); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	// One copy a day, taken once the server is answering so a large database
	// does not hold up opening Rowset. Shutdown interrupts the copy and waits
	// for it, so the store is never closed underneath it. Failing to write one
	// is not a reason to stop.
	snapshotted := make(chan struct{})
	go func() {
		defer close(snapshotted)
		if _, err := data.DailySnapshot(ctx, snapshots, 7); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "Rowset: could not write a snapshot of the database: %v\n", err)
		}
	}()
	defer func() {
		stop()
		<-snapshotted
	}()
	if os.Getenv("ROWSET_DESKTOP_NO_BROWSER") != "1" {
		if err := openDesktopState(directory); err != nil {
			stop()
			_ = server.Close()
			return err
		}
	}
	select {
	case <-ctx.Done():
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	_ = server.Close() // cancel any query still running after the shutdown deadline
	return nil
}

// spawnDetached re-execs this same executable as `desktop`, detached (see
// desktopDetachAttrs), then waits for it to report itself ready before
// returning - so a script or a shell prompt calling `rowset desktop`
// synchronously still sees a real failure if startup fails, but otherwise
// gets its prompt back immediately while the detached copy keeps running.
// It never opens the browser itself; the detached copy does that once it
// reaches the same point this process would have.
func spawnDetached(directory string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	statePath := filepath.Join(directory, "instance.json")
	var before time.Time
	if info, err := os.Stat(statePath); err == nil {
		before = info.ModTime()
	}
	cmd := exec.Command(exe, "desktop")
	cmd.Env = append(os.Environ(), "ROWSET_DETACHED=1")
	cmd.SysProcAttr = detachedAttrs()
	if devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		defer devNull.Close()
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start Rowset in the background: %w", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		info, err := os.Stat(statePath)
		if err == nil && info.ModTime().After(before) {
			raw, err := os.ReadFile(statePath)
			var state desktopState
			if err == nil && json.Unmarshal(raw, &state) == nil && state.Port > 0 && len(state.Key) == 64 {
				fmt.Fprintln(os.Stderr, "Rowset: started in the background; it keeps running after this terminal closes.")
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("Rowset did not report starting within 15s; check the logs in the data directory")
}

func openDesktopState(directory string) error {
	raw, err := os.ReadFile(filepath.Join(directory, "instance.json"))
	if err != nil {
		return err
	}
	var state desktopState
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	if state.Port < 1 || state.Port > 65535 || len(state.Key) != 64 {
		return errors.New("invalid instance state")
	}
	base := "http://127.0.0.1:" + strconv.Itoa(state.Port)
	req, err := http.NewRequest("POST", base+"/api/local/open", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+state.Key)
	client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("local instance did not accept the launcher")
	}
	var result struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		return err
	}
	if result.Ticket == "" {
		return errors.New("missing local ticket")
	}
	if os.Getenv("ROWSET_DESKTOP_NO_BROWSER") == "1" {
		return nil
	}
	// A new query value forces an already-open browser tab to request the
	// current index and hashed assets. Changing only the #local fragment keeps
	// the old JavaScript document alive after an in-place desktop upgrade.
	launchID := strconv.FormatInt(time.Now().UnixNano(), 10)
	return openBrowser(base + "/?desktop=" + launchID + "#local=" + result.Ticket)
}

func openBrowser(url string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		// Chrome and Edge can host the local UI in a standalone app window,
		// without tabs or an address bar. Prefer Chrome when both are present;
		// fall back to the Windows URL handler on machines without either.
		for _, browser := range windowsAppBrowsers() {
			if info, err := os.Stat(browser); err != nil || info.IsDir() {
				continue
			}
			app := exec.Command(browser, "--app="+url, "--start-maximized")
			if err := app.Start(); err == nil {
				_ = app.Process.Release()
				return nil
			}
		}
		// rundll32 url.dll,FileProtocolHandler is the classic trick but goes
		// through Internet Explorer's URL handler specifically on some
		// Windows builds (notably Windows Server) rather than the user's
		// actual default browser. `start` goes through the same ShellExecute
		// path a double-clicked link does, which respects it. The empty
		// string is a required placeholder: start treats a quoted first
		// argument as the window title, so without it a URL there would be
		// read as the title instead of the target.
		command = exec.Command("cmd", "/c", "start", "", url)
	case "linux":
		command = exec.Command("xdg-open", url)
	default:
		return fmt.Errorf("unsupported desktop platform %s", runtime.GOOS)
	}
	return command.Run()
}

func windowsAppBrowsers() []string {
	return []string{
		filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("LocalAppData"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
	}
}

func stopDesktop() error {
	directory, err := desktopDataDir()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(directory, "instance.json"))
	if err != nil {
		return err
	}
	var state desktopState
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	if state.Port < 1 || state.Port > 65535 || len(state.Key) != 64 {
		return errors.New("invalid instance state")
	}
	req, err := http.NewRequest("POST", "http://127.0.0.1:"+strconv.Itoa(state.Port)+"/api/local/stop?rollback=true", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+state.Key)
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 204 {
		return errors.New("could not stop local Rowset instance")
	}
	return nil
}
