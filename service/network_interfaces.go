package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

const networkInterfaceCacheTTL = 5 * time.Second

type NetworkInterfaceInfo struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	Families  []string `json:"families"`
	IsDefault bool     `json:"is_default"`
}

type physicalInterface struct {
	id        string
	name      string
	addresses []net.IP
	index4    uint32
	index6    uint32
	metric4   uint32
	metric6   uint32
	default4  bool
	default6  bool
}

type interfaceCatalog struct {
	mu       sync.Mutex
	loadedAt time.Time
	items    []physicalInterface
	loader   func() ([]physicalInterface, error)
	now      func() time.Time
	cacheFor time.Duration
}

func newInterfaceCatalog(loader func() ([]physicalInterface, error)) *interfaceCatalog {
	return &interfaceCatalog{loader: loader, now: time.Now, cacheFor: networkInterfaceCacheTTL}
}

var bypassInterfaceCatalog = newInterfaceCatalog(enumeratePlatformInterfaces)

func (c *interfaceCatalog) load() ([]physicalInterface, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.loadedAt.IsZero() || now.Sub(c.loadedAt) >= c.cacheFor {
		items, err := c.loader()
		if err != nil {
			return nil, err
		}
		c.items = clonePhysicalInterfaces(items)
		c.loadedAt = now
	}
	return clonePhysicalInterfaces(c.items), nil
}

func (c *interfaceCatalog) invalidate() {
	c.mu.Lock()
	c.loadedAt = time.Time{}
	c.items = nil
	c.mu.Unlock()
}

func clonePhysicalInterfaces(items []physicalInterface) []physicalInterface {
	cloned := make([]physicalInterface, len(items))
	for index := range items {
		cloned[index] = items[index]
		cloned[index].addresses = make([]net.IP, len(items[index].addresses))
		for addressIndex, address := range items[index].addresses {
			cloned[index].addresses[addressIndex] = append(net.IP(nil), address...)
		}
	}
	return cloned
}

func listPhysicalNetworkInterfaces() ([]NetworkInterfaceInfo, error) {
	items, err := bypassInterfaceCatalog.load()
	if err != nil {
		return nil, fmt.Errorf("enumerate physical network interfaces: %w", err)
	}
	result := make([]NetworkInterfaceInfo, 0, len(items))
	for _, item := range items {
		info := NetworkInterfaceInfo{ID: item.id, Name: item.name, IsDefault: item.default4 || item.default6}
		if item.index4 != 0 && item.addressForFamily(4) != nil {
			info.Families = append(info.Families, "ipv4")
		}
		if item.index6 != 0 && item.addressForFamily(6) != nil {
			info.Families = append(info.Families, "ipv6")
		}
		for _, address := range item.addresses {
			info.Addresses = append(info.Addresses, address.String())
		}
		result = append(result, info)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].IsDefault != result[j].IsDefault {
			return result[i].IsDefault
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}

func (p physicalInterface) addressForFamily(family int) net.IP {
	for _, address := range p.addresses {
		if ipFamily(address) == family {
			return append(net.IP(nil), address...)
		}
	}
	return nil
}

func (p physicalInterface) indexForFamily(family int) uint32 {
	if family == 4 {
		return p.index4
	}
	return p.index6
}

func (p physicalInterface) defaultForFamily(family int) bool {
	if family == 4 {
		return p.default4
	}
	return p.default6
}

func (p physicalInterface) metricForFamily(family int) uint32 {
	if family == 4 {
		return p.metric4
	}
	return p.metric6
}

func selectPhysicalInterface(items []physicalInterface, requestedID string, family int) (physicalInterface, net.IP, error) {
	requestedID = strings.TrimSpace(requestedID)
	if requestedID != "" {
		for _, item := range items {
			if item.id != requestedID && !strings.EqualFold(item.id, requestedID) {
				continue
			}
			address := item.addressForFamily(family)
			if item.indexForFamily(family) == 0 || address == nil {
				return physicalInterface{}, nil, newTUNBypassError("指定网卡 %q 不支持目标所需的 IPv%d", item.name, family)
			}
			return item, address, nil
		}
		return physicalInterface{}, nil, newTUNBypassError("指定网卡已断开或不存在，请重新选择")
	}

	candidates := make([]physicalInterface, 0, len(items))
	for _, item := range items {
		if item.indexForFamily(family) != 0 && item.addressForFamily(family) != nil {
			candidates = append(candidates, item)
		}
	}
	if len(candidates) == 0 {
		return physicalInterface{}, nil, newTUNBypassError("未找到支持 IPv%d 的可用物理网卡", family)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		leftDefault := candidates[i].defaultForFamily(family)
		rightDefault := candidates[j].defaultForFamily(family)
		if leftDefault != rightDefault {
			return leftDefault
		}
		leftMetric := candidates[i].metricForFamily(family)
		rightMetric := candidates[j].metricForFamily(family)
		if leftMetric != rightMetric {
			return leftMetric < rightMetric
		}
		return strings.ToLower(candidates[i].name) < strings.ToLower(candidates[j].name)
	})
	selected := candidates[0]
	return selected, selected.addressForFamily(family), nil
}

type bypassDialPlan struct {
	interfaceInfo physicalInterface
	sourceIP      net.IP
	family        int
}

func prepareBypassDialPlan(resolvedIP, requestedInterfaceID string) (bypassDialPlan, error) {
	ip := net.ParseIP(resolvedIP)
	if ip == nil {
		return bypassDialPlan{}, newTUNBypassError("目标地址 %q 不是有效 IP", resolvedIP)
	}
	family := ipFamily(ip)
	items, err := bypassInterfaceCatalog.load()
	if err != nil {
		return bypassDialPlan{}, newTUNBypassError("读取物理网卡失败：%v", err)
	}
	selected, sourceIP, err := selectPhysicalInterface(items, requestedInterfaceID, family)
	if err != nil {
		return bypassDialPlan{}, err
	}
	return bypassDialPlan{interfaceInfo: selected, sourceIP: sourceIP, family: family}, nil
}

func (p bypassDialPlan) dial(ctx context.Context, address string) (net.Conn, error) {
	dialer := &net.Dialer{
		LocalAddr: &net.TCPAddr{IP: append(net.IP(nil), p.sourceIP...)},
		Control: func(_, _ string, raw syscall.RawConn) error {
			var optionErr error
			if err := raw.Control(func(fd uintptr) {
				optionErr = configureBoundSocket(fd, p.family, p.interfaceInfo)
			}); err != nil {
				return newTUNBypassError("无法控制探测 Socket：%v", err)
			}
			if optionErr != nil {
				return newTUNBypassError("无法绑定网卡 %q：%v", p.interfaceInfo.name, optionErr)
			}
			return nil
		},
	}
	network := "tcp4"
	if p.family == 6 {
		network = "tcp6"
	}
	connection, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		if isSourceAddressUnavailable(err) {
			return nil, newTUNBypassError("网卡 %q 的本地地址已不可用", p.interfaceInfo.name)
		}
		return nil, err
	}
	local, ok := connection.LocalAddr().(*net.TCPAddr)
	if !ok || local.IP == nil || !local.IP.Equal(p.sourceIP) {
		_ = connection.Close()
		return nil, newTUNBypassError("Socket 未使用预期的物理网卡地址 %s", p.sourceIP)
	}
	return connection, nil
}

type tunBypassError struct {
	message string
}

func (e *tunBypassError) Error() string {
	return "绕过 TUN 失败：" + e.message
}

func newTUNBypassError(format string, values ...any) error {
	return &tunBypassError{message: fmt.Sprintf(format, values...)}
}

func isTUNBypassError(err error) bool {
	var target *tunBypassError
	return errors.As(err, &target)
}

func isSourceAddressUnavailable(err error) bool {
	return errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, wsaeAddrNotAvailable)
}

func shouldInvalidateInterfaceCache(err error) bool {
	if isTUNBypassError(err) || isSourceAddressUnavailable(err) {
		return true
	}
	return errors.Is(err, syscall.ENETDOWN) || errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, wsaeNetworkDown) ||
		errors.Is(err, wsaeNetworkUnreach) || errors.Is(err, wsaeHostUnreach)
}

func ipFamily(ip net.IP) int {
	if ip.To4() != nil {
		return 4
	}
	return 6
}

func usableSourceIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

func isLikelyVirtualInterface(values ...string) bool {
	combined := strings.ToLower(strings.Join(values, " "))
	markers := []string{
		"wintun", "wireguard", "sing-box", "singbox", "v2ray", "xray",
		"tailscale", "zerotier", "openvpn", "tap-windows", "vpn",
		"hyper-v", "vethernet", "vmware", "virtualbox", "loopback",
		"utun", "ipsec", "ppp", "tunnel",
	}
	for _, marker := range markers {
		if strings.Contains(combined, marker) {
			return true
		}
	}
	return false
}

var fakeIPPrefixes = []netip.Prefix{
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("fc00::/18"),
}

func resolvedContainsLikelyFakeIP(host string, addresses []string) bool {
	trimmed := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if net.ParseIP(trimmed) != nil {
		return false
	}
	for _, value := range addresses {
		address, err := netip.ParseAddr(value)
		if err != nil {
			continue
		}
		for _, prefix := range fakeIPPrefixes {
			if prefix.Contains(address) {
				return true
			}
		}
	}
	return false
}

func appendFakeIPHint(message string, likelyFakeIP bool) string {
	if !likelyFakeIP {
		return message
	}
	return message + "；系统 DNS 返回的地址疑似 FakeIP，绕过 TUN 时建议直接填写目标真实 IP"
}
