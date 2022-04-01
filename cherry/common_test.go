package cherry

import (
	"fmt"
	"math/rand"

	"github.com/cherryservers/cherrygo"
	"github.com/cherryservers/cloud-provider-cherry/cherry/server/store"
	randomdata "github.com/pallinder/go-randomdata"
)

var randomID = rand.Intn(1000)

// find a "EU-Nord-1" region or create it
func testGetOrCreateValidRegion(name, code string, backend store.DataStore) (*cherrygo.Region, error) {
	region, err := backend.GetRegionByCode(code)
	if err != nil {
		return nil, err
	}
	// if we already have it, use it
	if region != nil {
		return region, nil
	}
	// we do not have it, so create it
	return backend.CreateRegion(name, code)
}

// find a valid plan or create it
func testGetOrCreateValidPlan(name string, backend store.DataStore) (*cherrygo.Plans, error) {
	plan, err := backend.GetPlanByName(name)
	if err != nil {
		return nil, err
	}
	// if we already have it, use it
	if plan != nil {
		return plan, nil
	}
	// we do not have it, so create it
	return backend.CreatePlan(name)
}

// testGetNewServerName get a unique server name
func testGetNewServerName() string {
	return fmt.Sprintf("server-%d", rand.Intn(1000))
}

func testCreateAddress(ipv6, public bool) cherrygo.IPAddresses {
	family := 4
	if ipv6 {
		family = 6
	}
	ipaddr := ""
	if ipv6 {
		ipaddr = randomdata.IpV6Address()
	} else {
		ipaddr = randomdata.IpV4Address()
	}
	addrType := "primary-ip"
	if !public {
		addrType = "private-ip"
	}
	address := cherrygo.IPAddresses{
		Address:       ipaddr,
		AddressFamily: int(family),
		Type:          addrType,
	}
	return address
}
