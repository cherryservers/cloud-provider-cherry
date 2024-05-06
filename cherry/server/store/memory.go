package store

import (
	"fmt"
	"sync"

	cherrygo "github.com/cherryservers/cherrygo/v3"
)

// Memory is an implementation of DataStore which stores everything in memory
type Memory struct {
	mu       sync.Mutex
	counter  int
	regions  map[int]*cherrygo.Region
	servers  map[int]*cherrygo.Server
	plans    map[int]*cherrygo.Plan
	projects map[int]*cherrygo.Project
}

// NewMemory returns a properly initialized Memory
func NewMemory() *Memory {
	mem := &Memory{
		regions:  map[int]*cherrygo.Region{},
		servers:  map[int]*cherrygo.Server{},
		plans:    map[int]*cherrygo.Plan{},
		projects: map[int]*cherrygo.Project{},
	}
	// create default plan
	_, _ = mem.CreatePlan("baremetal")
	// create default region
	_, _ = mem.CreateRegion("EU-Nord-1", "LT")
	return mem
}

// getID get new unique number ID
func (m *Memory) getID() int {
	m.mu.Lock()
	m.counter++
	value := m.counter
	m.mu.Unlock()
	return value
}

// CreateFacility creates a new region
func (m *Memory) CreateRegion(name, code string) (*cherrygo.Region, error) {
	region := &cherrygo.Region{
		ID:         m.getID(),
		Name:       name,
		RegionIso2: code,
	}
	m.regions[region.ID] = region
	return region, nil
}

// ListRegions returns regions; if blank, it knows about ewr1
func (m *Memory) ListRegions() ([]*cherrygo.Region, error) {
	count := len(m.regions)
	if count != 0 {
		regions := make([]*cherrygo.Region, 0, count)
		for _, v := range m.regions {
			if len(regions) >= count {
				break
			}
			regions = append(regions, v)
		}
		return regions, nil
	}
	return nil, nil
}

// GetRegion get a single region by ID
func (m *Memory) GetRegion(id int) (*cherrygo.Region, error) {
	if region, ok := m.regions[id]; ok {
		return region, nil
	}
	return nil, nil
}

// GetRegionByName get a single region by name
func (m *Memory) GetRegionByName(name string) (*cherrygo.Region, error) {
	for _, r := range m.regions {
		if r.Name == name {
			return r, nil
		}
	}
	return nil, nil
}

// GetRegionByCode get a single region by name
func (m *Memory) GetRegionByCode(code string) (*cherrygo.Region, error) {
	for _, r := range m.regions {
		if r.RegionIso2 == code {
			return r, nil
		}
	}
	return nil, nil
}

// CreatePlan create a single plan
func (m *Memory) CreatePlan(name string) (*cherrygo.Plan, error) {
	plan := &cherrygo.Plan{
		ID:   m.getID(),
		Name: name,
	}
	m.plans[plan.ID] = plan
	return plan, nil
}

// ListPlans list all plans
func (m *Memory) ListPlans() ([]*cherrygo.Plan, error) {
	count := len(m.plans)
	if count != 0 {
		plans := make([]*cherrygo.Plan, 0, count)
		for _, v := range m.plans {
			if len(plans) >= count {
				break
			}
			plans = append(plans, v)
		}
		return plans, nil
	}
	return nil, nil
}

// GetPlan get plan by ID
func (m *Memory) GetPlan(id int) (*cherrygo.Plan, error) {
	if plan, ok := m.plans[id]; ok {
		return plan, nil
	}
	return nil, nil
}

// GetPlanByName get plan by name
func (m *Memory) GetPlanByName(name string) (*cherrygo.Plan, error) {
	for _, p := range m.plans {
		if p.Name == name {
			return p, nil
		}
	}
	return nil, nil
}

// CreateServer creates a new server
func (m *Memory) CreateServer(projectID int, name string, plan cherrygo.Plan, region cherrygo.Region) (*cherrygo.Server, error) {
	server := &cherrygo.Server{
		ID:       m.getID(),
		Hostname: name,
		State:    "active",
		Region:   region,
		Plan:     plan,
		Project: cherrygo.Project{
			ID: projectID,
		},
	}
	m.servers[server.ID] = server
	return server, nil
}

// UpdateServer updates an existing device
func (m *Memory) UpdateServer(ID int, server *cherrygo.Server) error {
	if server == nil {
		return fmt.Errorf("must include a valid server")
	}
	if _, ok := m.servers[server.ID]; ok {
		m.servers[ID] = server
		return nil
	}
	return fmt.Errorf("server not found")
}

// ListServers list all known servers for the project
func (m *Memory) ListServers(projectID int) ([]*cherrygo.Server, error) {
	count := len(m.servers)
	servers := make([]*cherrygo.Server, 0, count)
	for _, v := range m.servers {
		if len(servers) >= count {
			break
		}
		if v.Project.ID != projectID {
			continue
		}
		servers = append(servers, v)
	}
	return servers, nil
}

// GetServer get information about a single server
func (m *Memory) GetServer(serverID int) (*cherrygo.Server, error) {
	if server, ok := m.servers[serverID]; ok {
		return server, nil
	}
	return nil, nil
}

// DeleteServer delete a single server
func (m *Memory) DeleteServer(serverID int) (bool, error) {
	if _, ok := m.servers[serverID]; ok {
		delete(m.servers, serverID)
		return true, nil
	}
	return false, nil
}

func (m *Memory) CreateProject(name string, bgp bool) (*cherrygo.Project, error) {
	project := &cherrygo.Project{
		Name: name,
		ID:   m.getID(),
		Bgp: cherrygo.ProjectBGP{
			Enabled: bgp,
		},
	}
	m.projects[project.ID] = project
	return project, nil
}

func (m *Memory) GetProject(ID int) (*cherrygo.Project, error) {
	if project, ok := m.projects[ID]; ok {
		return project, nil
	}
	return nil, nil
}

func (m *Memory) UpdateProject(ID int, project *cherrygo.Project) error {
	if project == nil {
		return fmt.Errorf("must include a valid project")
	}
	if ID != project.ID {
		return fmt.Errorf("project ID mismatch")
	}
	if _, ok := m.projects[project.ID]; ok {
		m.projects[project.ID] = project
		return nil
	}
	return fmt.Errorf("project not found")
}
