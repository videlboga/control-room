package orchestrator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"control-room/internal/epic"
	"control-room/internal/store"
)

// WatchCron scans all in-progress epics and starts a detached watch for each
// one that is not already watched (lockfile + alive process).
type WatchCron struct {
	Store     *store.Store
	Executable string
}

// Run performs one sweep. It returns the number of epics it attempted to start.
func (wc *WatchCron) Run() (int, error) {
	if wc.Executable == "" {
		self, err := os.Executable()
		if err != nil {
			return 0, err
		}
		wc.Executable = self
	}
	epics, err := epic.List(wc.Store)
	if err != nil {
		return 0, err
	}
	lockDir := filepath.Join(wc.Store.Root, ".watch-lock")
	_ = os.MkdirAll(lockDir, 0o755)
	started := 0
	for _, e := range epics {
		if e.Status != "in_progress" {
			continue
		}
		lockPath := filepath.Join(lockDir, e.ID+".lock")
		if isLocked(lockPath) {
			continue
		}
		pid, err := wc.startWatch(e.ID)
		if err != nil {
			_ = writeLock(lockPath, -1, err.Error())
			continue
		}
		_ = writeLock(lockPath, pid, "")
		started++
	}
	return started, nil
}

func (wc *WatchCron) startWatch(epicID string) (int, error) {
	args := []string{"orchestrate", "watch", "--epic", epicID}
	c := exec.Command(wc.Executable, args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = append(os.Environ(), "CONTROL_ROOM_WATCH_DETACHED=1")
	if err := c.Start(); err != nil {
		return 0, err
	}
	return c.Process.Pid, nil
}

func isLocked(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return false
	}
	pid, _ := strconv.Atoi(fields[0])
	if pid <= 0 {
		return false
	}
	// Check if process is alive.
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

func writeLock(path string, pid int, errMsg string) error {
	content := fmt.Sprintf("%d", pid)
	if errMsg != "" {
		content = fmt.Sprintf("-1 %s %s", time.Now().UTC().Format(time.RFC3339), errMsg)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
