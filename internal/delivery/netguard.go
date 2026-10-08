package delivery

import (
	"fmt"
	"net"
	"net/url"
	"syscall"
	"time"
)

var (
	linkLocal4 = mustCIDR("169.254.0.0/16") // includes cloud metadata at 169.254.169.254
	linkLocal6 = mustCIDR("fe80::/10")
	cgnat      = mustCIDR("100.64.0.0/10")
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// blockedIP reports whether a webhook may not connect to ip. Link-local
// (including the cloud metadata address) is always blocked. Loopback, private
// and similar ranges are blocked unless allowPrivate is set (dev and tests).
func blockedIP(ip net.IP, allowPrivate bool) bool {
	if linkLocal4.Contains(ip) || linkLocal6.Contains(ip) || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if allowPrivate {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || cgnat.Contains(ip)
}

// guardedDialer checks the resolved address at connect time, so a hostname
// that resolves to a forbidden address (DNS rebinding) is refused too.
func guardedDialer(allowPrivate bool) *net.Dialer {
	return &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || blockedIP(ip, allowPrivate) {
				return fmt.Errorf("delivery: connection to %s refused by the webhook network policy", host)
			}
			return nil
		},
	}
}

// ValidateURL checks a webhook URL when it is registered.
func ValidateURL(raw string, allowPrivate bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("url must be absolute, without credentials")
	}
	if u.Scheme != "https" && (!allowPrivate || u.Scheme != "http") {
		return nil, fmt.Errorf("url must use https")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && blockedIP(ip, allowPrivate) {
		return nil, fmt.Errorf("url points at an address webhooks may not use")
	}
	return u, nil
}
