package cherry

import (
	"github.com/cherryservers/cherrygo"
)

// ipReservationByAllTags given a set of cherrygo.IPAddresses and a set of tags, find
// the first reservation that has all of those tags
func ipReservationByAllTags(targetTags map[string]string, ips []cherrygo.IPAddresses) *cherrygo.IPAddresses {
	ret := ipReservationsByAllTags(targetTags, ips)
	if len(ret) > 0 {
		return ret[0]
	}
	return nil
}

// ipReservationsByAllTags given a set of cherrygo.IPAddresses and a set of tags, find
// all of the reservations that have all of those tags
func ipReservationsByAllTags(targetTags map[string]string, ips []cherrygo.IPAddresses) []*cherrygo.IPAddresses {
	// cycle through the IPs, looking for one that matches ours
	ret := []*cherrygo.IPAddresses{}
ips:
	for i, ip := range ips {
		for k, v := range targetTags {
			// if it does not have this tag, or the tag does not equal the value, this is not a candidate
			if tagv, ok := ip.Tags[k]; !ok || v != tagv {
				continue ips
			}
		}
		// if we made it this far, the IP must have all the same tags, and with the same value
		ret = append(ret, &ips[i])
	}
	return ret
}
