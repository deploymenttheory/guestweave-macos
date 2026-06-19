// Port of tart's DeviceInfo/DeviceInfo.swift. The Sysctl package becomes
// syscall.Sysctl("hw.model").
//go:build darwin

package platform

import (
	"sync"
	"syscall"
)

// DeviceInfoOS ports DeviceInfo.os (memoized).
var DeviceInfoOS = sync.OnceValue(func() string {
	version, err := syscall.Sysctl("kern.osproductversion")
	if err != nil {
		return "macOS unknown"
	}
	return "macOS " + version
})

// DeviceInfoModel ports DeviceInfo.model (memoized).
var DeviceInfoModel = sync.OnceValue(func() string {
	model, err := syscall.Sysctl("hw.model")
	if err != nil {
		return "unknown"
	}
	return model
})
