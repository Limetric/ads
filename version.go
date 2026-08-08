package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// version is overridden at build time:
//
//	go build -ldflags "-X main.version=v0.1.0" -o build/ads .
//
// When unset, it falls back to the module version embedded by the Go toolchain.
var version = "dev"

func versionString() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func versionVerboseString() string {
	s := fmt.Sprintf("ads %s\n  go: %s\n  platform: %s/%s", versionString(), runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, kv := range info.Settings {
			switch kv.Key {
			case "vcs.revision", "vcs.time", "vcs.modified":
				s += fmt.Sprintf("\n  %s: %s", kv.Key, kv.Value)
			}
		}
	}
	return s
}
