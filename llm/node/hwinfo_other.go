//go:build !linux && !darwin

package node

import "github.com/lesismal/fit/agent/internal/model"

func collectPlatform(*model.HWInfo)                     {}
func collectPlatformGPUs(*model.HWInfo) []model.GPUInfo { return nil }
func collectDisks() []model.DiskInfo                    { return nil }
