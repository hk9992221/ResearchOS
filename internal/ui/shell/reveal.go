package shell

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"researchos/internal/i18n"
	"researchos/internal/ui/app"
	"researchos/internal/ui/errtext"
)

// openProjectFolder shows a project's folder in the system file manager. The
// click returns at once like openProject does: the path resolves and the OS
// viewer starts off the UI thread. Failures arrive as a toast naming the path.
func openProjectFolder(a *app.App, id string) {
	go func() {
		dir, err := a.ProjectDir(id)
		if err == nil {
			err = revealFolder(dir)
		}
		if err != nil {
			message := errtext.Text(err)
			if dir != "" {
				message = dir + ": " + message
			}
			a.Toast(app.LevelDanger, i18n.T("shell.folder.toast_fail"), message)
		}
	}()
}

// revealFolder opens dir with the OS file manager: Explorer on Windows,
// Finder on macOS, xdg-open elsewhere. The command is started, never waited
// on: explorer.exe exits non-zero even on success, and a file manager runs
// for as long as the user leaves it open.
func revealFolder(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("shell: %s 不是文件夹", dir)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", dir)
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	return cmd.Start()
}
