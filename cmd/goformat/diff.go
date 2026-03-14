package main

import (
	"github.com/aymanbagabas/go-udiff"
)

func diff(a, b []byte, path string) ([]byte, error) {
	d := udiff.Unified(path, path, string(a), string(b))
	return []byte(d), nil
}
