package autostart

import (
	"runtime"
	"testing"
)

func TestIsTempBuildBinary(t *testing.T) {
	if !IsTempBuildBinary("/private/var/folders/xx/go-build12345/b001/exe/main") {
		t.Fatalf("expected go-build path to be detected as temp binary")
	}
	if !IsTempBuildBinary(`C:\Users\Admin\AppData\Local\Temp\go-build999\b001\autostart.test.exe`) {
		t.Fatalf("expected .test.exe path to be detected as temp binary")
	}
	if IsTempBuildBinary(`D:\Tools\FocusCamera\FocusCamera-Windows-x64.exe`) {
		t.Fatalf("did not expect normal exe path to be detected as temp binary")
	}
}

func TestCommandMatchesExecutable(t *testing.T) {
	exe := "/Applications/FocusCamera.app/Contents/MacOS/FocusCamera"
	if !commandMatchesExecutable(exe, exe) {
		t.Fatalf("expected direct match")
	}
	if !commandMatchesExecutable(`"/Applications/FocusCamera.app/Contents/MacOS/FocusCamera" --autostart`, exe) {
		t.Fatalf("expected quoted --autostart match")
	}
	if commandMatchesExecutable("/OldFolder/FocusCamera.app/Contents/MacOS/FocusCamera", exe) {
		t.Fatalf("expected moved path to not match old path")
	}
	if runtime.GOOS == "darwin" {
		st := GetStatus(false, "window")
		if !st.Supported {
			t.Fatalf("expected darwin to support autostart")
		}
		if st.Mode != "window" {
			t.Fatalf("expected normalized mode 'window', got %q", st.Mode)
		}
	}
}
