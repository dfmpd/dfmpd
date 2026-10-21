//go:build !linux

package plugin

import (
	"errors"
	"os/exec"
)

func isolateProcess(*exec.Cmd) error {
	return errors.New("external Playback process supervision requires Linux")
}
func observeExit(*exec.Cmd) error       { return errors.New("unsupported process supervision") }
func signalGroup(*exec.Cmd, bool) error { return errors.New("unsupported process supervision") }
