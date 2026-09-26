//go:build !linux

package goproc

import "context"

func reapOrphans(context.Context, func(pid int) bool) {}
