//go:build !windows

package config

import "os"

func atomicReplace(source, target string) error {
	return os.Rename(source, target)
}
