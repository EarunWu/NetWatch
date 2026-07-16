//go:build windows

package main

import (
	"fmt"
	"math/bits"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsUnicastInterfaceOption = 31

func enumeratePlatformInterfaces() ([]physicalInterface, error) {
	const flags = windows.GAA_FLAG_SKIP_ANYCAST |
		windows.GAA_FLAG_SKIP_MULTICAST |
		windows.GAA_FLAG_SKIP_DNS_SERVER |
		windows.GAA_FLAG_INCLUDE_GATEWAYS

	size := uint32(15 * 1024)
	for attempt := 0; attempt < 3; attempt++ {
		buffer := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, first, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return nil, err
		}
		return windowsPhysicalInterfaces(first), nil
	}
	return nil, fmt.Errorf("adapter list changed repeatedly while being read")
}

func windowsPhysicalInterfaces(first *windows.IpAdapterAddresses) []physicalInterface {
	result := make([]physicalInterface, 0, 4)
	for adapter := first; adapter != nil; adapter = adapter.Next {
		if adapter.OperStatus != windows.IfOperStatusUp || adapter.PhysicalAddressLength == 0 {
			continue
		}
		if adapter.IfType != windows.IF_TYPE_ETHERNET_CSMACD && adapter.IfType != windows.IF_TYPE_IEEE80211 {
			continue
		}
		id := windows.BytePtrToString(adapter.AdapterName)
		name := windows.UTF16PtrToString(adapter.FriendlyName)
		description := windows.UTF16PtrToString(adapter.Description)
		if id == "" || name == "" || isLikelyVirtualInterface(id, name, description) {
			continue
		}
		item := physicalInterface{
			id:      id,
			name:    name,
			index4:  adapter.IfIndex,
			index6:  adapter.Ipv6IfIndex,
			metric4: adapter.Ipv4Metric,
			metric6: adapter.Ipv6Metric,
		}
		for current := adapter.FirstUnicastAddress; current != nil; current = current.Next {
			ip := current.Address.IP()
			if !usableSourceIP(ip) {
				continue
			}
			item.addresses = append(item.addresses, append(net.IP(nil), ip...))
		}
		for gateway := adapter.FirstGatewayAddress; gateway != nil; gateway = gateway.Next {
			ip := gateway.Address.IP()
			if ip == nil || ip.IsUnspecified() {
				continue
			}
			if ip.To4() != nil {
				item.default4 = true
			} else {
				item.default6 = true
			}
		}
		if len(item.addresses) > 0 {
			result = append(result, item)
		}
	}
	return result
}

func configureBoundSocket(fd uintptr, family int, item physicalInterface) error {
	handle := windows.Handle(fd)
	index := item.indexForFamily(family)
	if index == 0 {
		return fmt.Errorf("interface has no IPv%d index", family)
	}
	if family == 4 {
		// Winsock expects the IPv4 interface index as a DWORD in network byte order.
		return windows.SetsockoptInt(handle, windows.IPPROTO_IP, windowsUnicastInterfaceOption, int(bits.ReverseBytes32(index)))
	}
	return windows.SetsockoptInt(handle, windows.IPPROTO_IPV6, windowsUnicastInterfaceOption, int(index))
}
