package e2etest

import (
	"os"
	"sync"
)

func fileCleanup(path string) func() {
	var once sync.Once
	return func() {
		once.Do(func() { os.Remove(path) })
	}
}