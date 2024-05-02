package store

import (
	cherrygo "github.com/cherryservers/cherrygo/v3"
)

// DataStore is the item that retrieves backend information to serve out
// following a contract API
type DataStore interface {
	CreateRegion(name, code string) (*cherrygo.Region, error)
	ListRegions() ([]*cherrygo.Region, error)
	GetRegion(ID int) (*cherrygo.Region, error)
	GetRegionByName(name string) (*cherrygo.Region, error)
	GetRegionByCode(code string) (*cherrygo.Region, error)
	CreatePlan(name string) (*cherrygo.Plan, error)
	ListPlans() ([]*cherrygo.Plan, error)
	GetPlan(ID int) (*cherrygo.Plan, error)
	GetPlanByName(name string) (*cherrygo.Plan, error)
	CreateServer(projectID int, name string, plan cherrygo.Plan, region cherrygo.Region) (*cherrygo.Server, error)
	UpdateServer(ID int, server *cherrygo.Server) error
	ListServers(projectID int) ([]*cherrygo.Server, error)
	GetServer(ID int) (*cherrygo.Server, error)
	DeleteServer(ID int) (bool, error)
	CreateProject(name string, bgp bool) (*cherrygo.Project, error)
	GetProject(ID int) (*cherrygo.Project, error)
	UpdateProject(ID int, project *cherrygo.Project) error
}
