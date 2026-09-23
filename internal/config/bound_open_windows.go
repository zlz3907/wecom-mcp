package config

import "os"

func openBoundConfig(path string) (*os.File, error) { return os.Open(path) }
