package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/cherryservers/cherrygo"
	"github.com/cherryservers/cloud-provider-cherry/cherry/server/store"
	"github.com/gorilla/mux"
)

// ErrorHandler a handler for errors that can choose to exit or not
// if it wants, it can exit entirely
type ErrorHandler interface {
	Error(error)
}

// CherryServer a handler creator for an http server
type CherryServer struct {
	Store store.DataStore
	ErrorHandler
}

type ErrorResponse struct {
	Response *http.Response
	Code     int    `json:"code"`
	Message  string `json:"message"`
}

// CreateHandler create an http.Handler
func (c *CherryServer) CreateHandler() http.Handler {
	r := mux.NewRouter()
	// list all regions
	r.HandleFunc("/v1/regions", c.listRegionsHandler).Methods("GET")
	// get individual region
	r.HandleFunc("/v1/regions/{regionID}", c.getRegionHandler).Methods("GET")
	// list all servers for a project
	r.HandleFunc("/v1/projects/{projectID}/servers", c.listServersHandler).Methods("GET")
	// get a single server
	r.HandleFunc("/v1/servers/{serverID}", c.getServerHandler).Methods("GET")
	// create a server
	r.HandleFunc("/v1/projects/{projectID}/server", c.createServerHandler).Methods("POST")
	// update a server
	r.HandleFunc("/v1/servers/{serverID}", c.updateServerHandler).Methods("PUT")
	// list all plans
	r.HandleFunc("/v1/plans", c.listPlansHandler).Methods("GET")
	// get individual plan
	r.HandleFunc("/v1/plans/{planID}", c.getPlanHandler).Methods("GET")
	// get individual project
	r.HandleFunc("/v1/projects/{projectID}", c.getProjectHandler).Methods("GET")
	// update individual project
	r.HandleFunc("/v1/projects/{projectID}", c.updateProjectHandler).Methods("PUT")
	return r
}

// list all regions
func (c *CherryServer) listRegionsHandler(w http.ResponseWriter, r *http.Request) {
	regions, err := c.Store.ListRegions()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "unable to list regions"})
		return
	}
	var resp = struct {
		regions []*cherrygo.Region
	}{
		regions: regions,
	}
	err = json.NewEncoder(w).Encode(&resp)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "unable to write json"})
		return
	}
}

// get individual region
func (c *CherryServer) getRegionHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	regionVar := vars["regionID"]
	ID, err := strconv.ParseInt(regionVar, 10, 32)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusBadRequest, Message: fmt.Sprintf("invalid region: %s", regionVar)})
		return
	}
	region, err := c.Store.GetRegion(int(ID))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusNotFound, Message: "region not found"})
		return
	}
	var resp = struct {
		region cherrygo.Region
	}{
		region: *region,
	}
	err = json.NewEncoder(w).Encode(&resp)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "unable to write json"})
		return
	}
}

// list all plans
func (c *CherryServer) listPlansHandler(w http.ResponseWriter, r *http.Request) {
	plans, err := c.Store.ListPlans()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "unable to list plans"})
		return
	}
	var resp = struct {
		plans []*cherrygo.Plans
	}{
		plans: plans,
	}
	err = json.NewEncoder(w).Encode(&resp)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "unable to write json"})
		return
	}
}

// get individual plan
func (c *CherryServer) getPlanHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	planVar := vars["planID"]
	ID, err := strconv.ParseInt(planVar, 10, 32)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusBadRequest, Message: fmt.Sprintf("invalid planID: %s", planVar)})
		return
	}
	plan, err := c.Store.GetPlan(int(ID))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusNotFound, Message: "plan not found"})
		return
	}
	var resp = struct {
		plan cherrygo.Plans
	}{
		plan: *plan,
	}
	err = json.NewEncoder(w).Encode(&resp)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "unable to write json"})
		return
	}
}

// list all servers for a project
func (c *CherryServer) listServersHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectVar := vars["projectID"]
	projectID, err := strconv.ParseInt(projectVar, 10, 32)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusBadRequest, Message: fmt.Sprintf("invalid project ID: %s", projectVar)})
		return
	}

	servers, err := c.Store.ListServers(int(projectID))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "project not found"})
		return
	}
	var resp = struct {
		Servers []*cherrygo.Server `json:"servers"`
	}{
		Servers: servers,
	}
	err = json.NewEncoder(w).Encode(&resp.Servers)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "unable to write json"})
		return
	}
}

// get information about a specific server
func (c *CherryServer) getServerHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverVar := vars["serverID"]
	ID, err := strconv.ParseInt(serverVar, 10, 32)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusBadRequest, Message: fmt.Sprintf("invalid server ID: %s", serverVar)})
		return
	}
	server, err := c.Store.GetServer(int(ID))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusNotFound, Message: "server not found"})
		return
	}
	if server != nil {
		err := json.NewEncoder(w).Encode(&server)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "unable to write json"})
			return
		}
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusNotFound, Message: "server not found"})
}

// create a server in a given project
func (c *CherryServer) createServerHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectVar := vars["projectID"]
	projectID, err := strconv.ParseInt(projectVar, 10, 32)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusBadRequest, Message: fmt.Sprintf("invalid project ID: %s", projectVar)})
		return
	}
	// read the body of the request
	var req cherrygo.CreateServer
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusBadRequest, Message: "cannot parse body of request"})
		return
	}
	// interpret the planID and see if it is a valid one
	planID, err := strconv.ParseInt(req.PlanID, 10, 32)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusBadRequest, Message: fmt.Sprintf("invalid plan ID: %s", req.PlanID)})
		return
	}
	plan, err := c.Store.GetPlan(int(planID))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusNotFound, Message: "unknown plan ID"})
		return
	}
	// interpret the region and see if it is a valid one
	region, err := c.Store.GetRegionByName(req.Region)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "unknown region"})
		return
	}
	server, err := c.Store.CreateServer(int(projectID), req.Hostname, *plan, *region)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "error creating server"})
		return
	}

	if server != nil {
		err := json.NewEncoder(w).Encode(&server)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "unable to write json"})
		}
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusNotFound, Message: "not found"})
}

// update a server
func (c *CherryServer) updateServerHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverVar := vars["serverID"]
	serverID, err := strconv.ParseInt(serverVar, 10, 32)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusBadRequest, Message: fmt.Sprintf("invalid server ID: %s", serverVar)})
		return
	}
	// read the body of the request
	var req cherrygo.UpdateServer
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusBadRequest, Message: "unable to parse body of request"})
		return
	}

	server, err := c.Store.GetServer(int(serverID))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusBadRequest, Message: "unknown server ID"})
		return
	}
	server.BGP.Enabled = req.Bgp
	if req.Tags != nil {
		server.Tags = *req.Tags
	}
	if err := c.Store.UpdateServer(int(serverID), server); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "unable to update server"})
		return
	}
	if server != nil {
		err := json.NewEncoder(w).Encode(&server)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "unable to write json"})
		}
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusNotFound, Message: "not found"})
}

// getProjectHandler get information about a specific project
func (c *CherryServer) getProjectHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectVar := vars["projectID"]
	ID, err := strconv.ParseInt(projectVar, 10, 32)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusBadRequest, Message: fmt.Sprintf("invalid project ID: %s", projectVar)})
		return
	}
	project, err := c.Store.GetProject(int(ID))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusNotFound, Message: "project ID not found"})
		return
	}
	if project != nil {
		err := json.NewEncoder(w).Encode(&project)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "unable to write json"})
		}
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusNotFound, Message: "project not found"})
}

// update a project
func (c *CherryServer) updateProjectHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	projectVar := vars["projectID"]
	projectID, err := strconv.ParseInt(projectVar, 10, 32)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusBadRequest, Message: fmt.Sprintf("invalid project ID: %s", projectVar)})
		return
	}
	// read the body of the request
	var req cherrygo.UpdateProject
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusBadRequest, Message: "unable to read body of request"})
		return
	}

	project, err := c.Store.GetProject(int(projectID))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusNotFound, Message: "project ID not found"})
		return
	}
	project.Bgp.Enabled = req.Bgp
	if req.Name != "" {
		project.Name = req.Name
	}
	if err := c.Store.UpdateProject(int(projectID), project); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "unable to update project"})
		return
	}
	if project != nil {
		err := json.NewEncoder(w).Encode(&project)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusInternalServerError, Message: "unable to write json"})
		}
		return
	}
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Code: http.StatusNotFound, Message: "not found"})
}
