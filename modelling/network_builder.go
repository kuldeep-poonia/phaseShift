package modelling

import "github.com/qphysics/phaseshift/topology"

func PopulateNetworkField(nf *NetworkField, topo topology.GraphSnapshot) {

	if nf == nil {
		return
	}

	if nf.Edges == nil {
		nf.Edges = make(map[string]*EdgeField)
	}

	// create one PDE edge per service
	for _, node := range topo.Nodes {

		id := node.ServiceID

		if _, ok := nf.Edges[id]; ok {
			continue
		}

		e := &EdgeField{
			Cells:       make([]Cell, 30),
			Dx:          1.0 / 30.0,
			ServiceRate: 0.15,
			NoiseAmp:    0.01,
			SourceGain:  0.4,
		}

		for i := range e.Cells {
			e.Cells[i].Rho = 0.08
		}

		nf.Edges[id] = e
	}

	// create junctions from call graph
	for _, edge := range topo.Edges {

		j := &Junction{
			In:  []string{edge.Source},
			Out: []string{edge.Target},
			R:   [][]float64{{1.0}},
		}

		nf.Junctions = append(nf.Junctions, j)
	}
}
