package store

import (
	"github.com/cherryservers/cherrygo"
)

// DataStore is the item that retrieves backend information to serve out
// following a contract API
type DataStore interface {
	CreateRegion(name, code string) (*cherrygo.Region, error)
	ListRegions() ([]*cherrygo.Region, error)
	GetRegion(ID int) (*cherrygo.Region, error)
	GetRegionByName(name string) (*cherrygo.Region, error)
	GetRegionByCode(code string) (*cherrygo.Region, error)
	CreatePlan(name string) (*cherrygo.Plans, error)
	ListPlans() ([]*cherrygo.Plans, error)
	GetPlan(ID int) (*cherrygo.Plans, error)
	GetPlanByName(name string) (*cherrygo.Plans, error)
	CreateServer(projectID int, name string, plan cherrygo.Plans, region cherrygo.Region) (*cherrygo.Server, error)
	UpdateServer(ID int, server *cherrygo.Server) error
	ListServers(projectID int) ([]*cherrygo.Server, error)
	GetServer(ID int) (*cherrygo.Server, error)
	DeleteServer(ID int) (bool, error)
	CreateProject(name string, bgp bool) (*cherrygo.Project, error)
	GetProject(ID int) (*cherrygo.Project, error)
	UpdateProject(ID int, project *cherrygo.Project) error
}
