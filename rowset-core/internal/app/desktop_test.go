package app

import (
	"path/filepath"
	"testing"
)

func TestDesktopLockExcludesSecondProcessAndCanBeReacquired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.lock")
	unlock, err := lockDesktop(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := lockDesktop(path); err == nil {
		second()
		unlock()
		t.Fatal("second lock acquired")
	}
	unlock()
	unlock, err = lockDesktop(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}
