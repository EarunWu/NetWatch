//go:build darwin

package main

import (
	"fmt"
	"net"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func enumeratePlatformInterfaces() ([]physicalInterface, error) {
	defaults := darwinDefaultInterfaceIndexes()
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	result := make([]physicalInterface, 0, len(interfaces))
	for _, candidate := range interfaces {
		if candidate.Flags&net.FlagUp == 0 || candidate.Flags&net.FlagLoopback != 0 {
			continue
		}
		// Physical Ethernet, Wi-Fi and USB adapters use enN BSD names on macOS.
		if !strings.HasPrefix(candidate.Name, "en") || isLikelyVirtualInterface(candidate.Name) {
			continue
		}
		addresses, err := candidate.Addrs()
		if err != nil {
			continue
		}
		item := physicalInterface{
			id:       candidate.Name,
			name:     candidate.Name,
			index4:   uint32(candidate.Index),
			index6:   uint32(candidate.Index),
			metric4:  uint32(candidate.Index),
			metric6:  uint32(candidate.Index),
			default4: defaults[4][candidate.Index],
			default6: defaults[6][candidate.Index],
		}
		for _, address := range addresses {
			var ip net.IP
			switch value := address.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if usableSourceIP(ip) {
				item.addresses = append(item.addresses, append(net.IP(nil), ip...))
			}
		}
		if len(item.addresses) > 0 {
			result = append(result, item)
		}
	}
	return result, nil
}

func darwinDefaultInterfaceIndexes() map[int]map[int]bool {
	result := map[int]map[int]bool{4: {}, 6: {}}
	raw, err := syscall.RouteRIB(syscall.AF_UNSPEC, syscall.NET_RT_DUMP)
	if err != nil {
		return result
	}
	messages, err := syscall.ParseRoutingMessage(raw)
	if err != nil {
		return result
	}
	for _, message := range messages {
		route, ok := message.(*syscall.RouteMessage)
		if !ok || route.Header.Flags&syscall.RTF_UP == 0 || route.Header.Flags&syscall.RTF_GATEWAY == 0 {
			continue
		}
		addresses, err := syscall.ParseRoutingSockaddr(route)
		if err != nil || len(addresses) <= syscall.RTAX_DST {
			continue
		}
		switch destination := addresses[syscall.RTAX_DST].(type) {
		case *syscall.SockaddrInet4:
			if destination.Addr == [4]byte{} {
				result[4][int(route.Header.Index)] = true
			}
		case *syscall.SockaddrInet6:
			if destination.Addr == [16]byte{} {
				result[6][int(route.Header.Index)] = true
			}
		}
	}
	return result
}

func configureBoundSocket(fd uintptr, family int, item physicalInterface) error {
	index := item.indexForFamily(family)
	if index == 0 {
		return fmt.Errorf("interface has no IPv%d index", family)
	}
	if family == 4 {
		return unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, int(index))
	}
	return unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, int(index))
}
