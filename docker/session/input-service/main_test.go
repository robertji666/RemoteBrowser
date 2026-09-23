package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWheelAccumulatorDropsOldDirectionOnReverse(t *testing.T) {
	wheels := newWheelAccumulator(newInputDriver(":1"), 50*time.Millisecond, 1)

	wheels.add(0, 720)
	wheels.add(0, 240)
	wheels.add(0, -120)

	deltaX, deltaY := wheels.take()
	if deltaX != 0 {
		t.Fatalf("deltaX = %v, want 0", deltaX)
	}
	if deltaY != -120 {
		t.Fatalf("deltaY = %v, want -120", deltaY)
	}
}

func TestWheelAccumulatorUsesDominantAxis(t *testing.T) {
	wheels := newWheelAccumulator(newInputDriver(":1"), 50*time.Millisecond, 1)

	wheels.add(180, 60)
	wheels.add(90, 400)

	deltaX, deltaY := wheels.take()
	if deltaX != 0 {
		t.Fatalf("deltaX = %v, want 0", deltaX)
	}
	if deltaY != 400 {
		t.Fatalf("deltaY = %v, want 400", deltaY)
	}
}

func TestWheelStepsCapsOutput(t *testing.T) {
	if got := wheelSteps(60, 1); got != 1 {
		t.Fatalf("wheelSteps(60) = %d, want 1", got)
	}
	if got := wheelSteps(1200, 1); got != 1 {
		t.Fatalf("wheelSteps(1200, 1) = %d, want 1", got)
	}
	if got := wheelSteps(1200, 4); got != 4 {
		t.Fatalf("wheelSteps(1200, 4) = %d, want 4", got)
	}
}

func TestMouseMoveAccumulatorKeepsLatestPosition(t *testing.T) {
	moves := newMouseMoveAccumulator(newInputDriver(":1"), 16*time.Millisecond)

	moves.add(10, 20)
	moves.add(30, 40)

	x, y, pending := moves.take()
	if !pending {
		t.Fatal("pending = false, want true")
	}
	if x != 30 || y != 40 {
		t.Fatalf("position = %d,%d, want 30,40", x, y)
	}

	_, _, pending = moves.take()
	if pending {
		t.Fatal("pending = true after take, want false")
	}
}

func TestIsASCII(t *testing.T) {
	if !isASCII("hello, world!") {
		t.Fatal("ASCII text reported as non-ASCII")
	}
	if isASCII("你好，世界") {
		t.Fatal("Unicode text reported as ASCII")
	}
}

func TestSetClipboardDoesNotWaitForSelectionOwner(t *testing.T) {
	dir := t.TempDir()
	xclipPath := filepath.Join(dir, "xclip")
	if err := os.WriteFile(xclipPath, []byte("#!/bin/sh\ncat >/dev/null\nsleep 30\n"), 0755); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Setenv("PATH", oldPath) }()

	driver := newInputDriver(":1")
	started := time.Now()
	if err := driver.setClipboard("你好"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("setClipboard took %s; selection owner should run asynchronously", elapsed)
	}

	driver.clipboardMu.Lock()
	cmd := driver.clipboardCmd
	driver.clipboardMu.Unlock()
	if cmd == nil || cmd.Process == nil {
		t.Fatal("clipboard owner process was not started")
	}
	_ = cmd.Process.Kill()
}
