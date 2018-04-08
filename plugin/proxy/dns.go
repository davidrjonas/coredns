package proxy

import (
	"fmt"
	"net"
	"time"

	"github.com/coredns/coredns/request"

	"github.com/miekg/dns"
	"golang.org/x/net/context"
)

type dnsEx struct {
	Timeout time.Duration
	Options
}

// Options define the options understood by dns.Exchange.
type Options struct {
	ForceTCP  bool // If true use TCP for upstream no matter what
	LocalAddr net.IP
}

func newDNSEx() *dnsEx {
	return newDNSExWithOption(Options{})
}

func newDNSExWithOption(opt Options) *dnsEx {
	return &dnsEx{Timeout: defaultTimeout * time.Second, Options: opt}
}

func (d *dnsEx) Transport() string {
	if d.Options.ForceTCP {
		return "tcp"
	}

	// The protocol will be determined by `state.Proto()` during Exchange.
	return ""
}
func (d *dnsEx) Protocol() string          { return "dns" }
func (d *dnsEx) OnShutdown(p *Proxy) error { return nil }
func (d *dnsEx) OnStartup(p *Proxy) error  { return nil }

// Exchange implements the Exchanger interface.
func (d *dnsEx) Exchange(ctx context.Context, addr string, state request.Request) (*dns.Msg, error) {
	proto := state.Proto()
	if d.Options.ForceTCP {
		proto = "tcp"
	}

	var dialer net.Dialer
	if proto == "tcp" {
		dialer = net.Dialer{Timeout: d.Timeout, LocalAddr: &net.TCPAddr{IP: d.LocalAddr}}
	} else if proto == "udp" {
		dialer = net.Dialer{Timeout: d.Timeout, LocalAddr: &net.UDPAddr{IP: d.LocalAddr}}
	} else {
		return nil, fmt.Errorf("expected tcp or udp, received unsupported proto '%s'", proto)
	}

	co, err := dialer.Dial(proto, addr)
	if err != nil {
		return nil, err
	}

	reply, _, err := d.ExchangeConn(state.Req, co)

	co.Close()

	if reply != nil && reply.Truncated {
		// Suppress proxy error for truncated responses
		err = nil
	}

	if err != nil {
		return nil, err
	}
	reply.Compress = true
	reply.Id = state.Req.Id
	// When using force_tcp the upstream can send a message that is too big for
	// the udp buffer, hence we need to truncate the message to at least make it
	// fit the udp buffer.
	reply, _ = state.Scrub(reply)

	return reply, nil
}

func (d *dnsEx) ExchangeConn(m *dns.Msg, co net.Conn) (*dns.Msg, time.Duration, error) {
	start := time.Now()
	r, err := exchange(m, co)
	rtt := time.Since(start)

	return r, rtt, err
}

func exchange(m *dns.Msg, co net.Conn) (*dns.Msg, error) {
	opt := m.IsEdns0()

	udpsize := uint16(dns.MinMsgSize)
	// If EDNS0 is used use that for size.
	if opt != nil && opt.UDPSize() >= dns.MinMsgSize {
		udpsize = opt.UDPSize()
	}

	dnsco := &dns.Conn{Conn: co, UDPSize: udpsize}

	writeDeadline := time.Now().Add(defaultTimeout)
	dnsco.SetWriteDeadline(writeDeadline)
	dnsco.WriteMsg(m)

	readDeadline := time.Now().Add(defaultTimeout)
	co.SetReadDeadline(readDeadline)
	r, err := dnsco.ReadMsg()

	dnsco.Close()
	if r == nil {
		return nil, err
	}
	return r, err
}
