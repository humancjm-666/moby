package network

import (
	"net"
	"testing"

	"github.com/miekg/dns"
	"gotest.tools/v3/assert"
)

const DNSRespAddr = "10.11.12.13"

// GenResolvConf generates a resolv.conf that only contains a single
// nameserver line, with address addr, and returns the file content.
func GenResolvConf(addr string) string {
	return "nameserver " + addr + "\n"
}

// StartDaftDNS starts and returns a really, really daft DNS server that only
// responds to type-A requests, and always with address dnsRespAddr.
// The DNS server will be stopped automatically by a t.Cleanup().
func StartDaftDNS(t *testing.T, addr string) {
	serveDNS := func(w dns.ResponseWriter, query *dns.Msg) {
		if query.Question[0].Qtype == dns.TypeA {
			resp := &dns.Msg{}
			resp.SetReply(query)
			answer := &dns.A{
				Hdr: dns.RR_Header{
					Name:   query.Question[0].Name,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    600,
				},
			}
			answer.A = net.ParseIP(DNSRespAddr)
			resp.Answer = append(resp.Answer, answer)
			_ = w.WriteMsg(resp)
		}
	}

	conn, err := net.ListenUDP("udp", &net.UDPAddr{
		IP:   net.ParseIP(addr),
		Port: 53,
	})
	assert.NilError(t, err)

	server := &dns.Server{Handler: dns.HandlerFunc(serveDNS), PacketConn: conn}
	go func() {
		_ = server.ActivateAndServe()
	}()

	t.Cleanup(func() { server.Shutdown() })
}
