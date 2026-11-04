package platform

import (
	"errors"
	"strings"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

const loginRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func loginCommand(executable string) (string, error) {
	if err := validateExecutable(executable); err != nil {
		return "", err
	}
	if strings.ContainsRune(executable, '"') {
		return "", errors.New("autostart executable contains a quote")
	}
	return syscall.EscapeArg(executable) + " --hidden", nil
}

func setRunAtLogin(on bool, executable string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, loginRunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		err := k.DeleteValue(AppID)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	command, err := loginCommand(executable)
	if err != nil {
		return err
	}
	return k.SetStringValue(AppID, command)
}

func runAtLoginEnabled(executable string) (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, loginRunKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer k.Close()
	value, _, err := k.GetStringValue(AppID)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	want, err := loginCommand(executable)
	return value == want && err == nil, err
}
