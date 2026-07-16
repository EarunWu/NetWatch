//go:build !windows && !darwin

package main

import "fmt"

func enumeratePlatformInterfaces() ([]physicalInterface, error) {
	return nil, fmt.Errorf("TUN bypass is supported only on Windows and macOS")
}

func configureBoundSocket(uintptr, int, physicalInterface) error {
	return fmt.Errorf("TUN bypass is supported only on Windows and macOS")
}
