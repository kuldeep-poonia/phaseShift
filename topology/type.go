package topology

import "time"

type Node struct {
	ServiceID      string
	LastSeen       time.Time
	NormalisedLoad float64
}

type Edge struct {
	Source      string
	Target      string
	CallRate    float64
	ErrorRate   float64
	LatencyMs   float64
	Weight      float64
	LastUpdated time.Time
}

type CriticalPath struct {
	Nodes       []string
	TotalWeight float64
	CascadeRisk float64
}

type GraphSnapshot struct {
	CapturedAt   time.Time
	Nodes        []Node
	Edges        []Edge
	CriticalPath CriticalPath
}
