package cherry

import (
	"errors"
	"net/netip"

	cherrygo "github.com/cherryservers/cherrygo/v3"
)

const (
	serverPublicIPType  = "primary-ip"
	serverPrivateIPType = "private-ip"
)

type serverIPs struct {
	public  []netip.Addr
	private []netip.Addr
}

func ipsFromServer(s cherrygo.Server) (serverIPs, error) {
	// Server most likely has a single public and private IP address.
	public, private := make([]netip.Addr, 0, 1), make([]netip.Addr, 0, 1)
	var errs []error

	for _, ip := range s.IPAddresses {
		addr, err := netip.ParseAddr(ip.Address)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		// We don't care about IP types other than "private" and "public" for now.
		switch ip.Type {
		case serverPublicIPType:
			public = append(public, addr)
		case serverPrivateIPType:
			private = append(private, addr)
		}
	}

	return serverIPs{
		public:  public,
		private: private,
	}, errors.Join(errs...)
}

func (s serverIPs) anyPublic4() (netip.Addr, error) {
	for _, ip := range s.public {
		if ip.Is4() {
			return ip, nil
		}
	}
	return netip.Addr{}, errors.New("server doesn't have a public IPv4")
}

func (s serverIPs) allPublic4() []netip.Addr {
	var pub4 []netip.Addr

	for _, ip := range s.public {
		if ip.Is4() {
			pub4 = append(pub4, ip)
		}
	}

	return pub4
}

func (s serverIPs) allPrivate4() []netip.Addr {
	var pri4 []netip.Addr

	for _, ip := range s.private {
		if ip.Is4() {
			pri4 = append(pri4, ip)
		}
	}

	return pri4
}
