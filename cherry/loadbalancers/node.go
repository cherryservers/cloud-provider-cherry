package loadbalancers

type Node struct {
	Name     string
	SourceIP string
	LocalASN int
	PeerASN  int
	Password string
	Peers    []Peer
	Region   string
}

type Peer struct {
	Address string
	Port    int
}
