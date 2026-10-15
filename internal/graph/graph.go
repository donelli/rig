// Package graph builds the service dependency DAG and provides a
// deterministic topological order plus cycle detection.
package graph

import (
	"fmt"
	"sort"
)

type Node struct {
	Name      string
	DependsOn []string
}

type Graph struct {
	nodes map[string]Node
	order []string
}

// Build validates that every dependency exists, detects cycles, and returns
// a Graph with a deterministic topological order.
func Build(nodes []Node) (*Graph, error) {
	byName := make(map[string]Node, len(nodes))
	for _, n := range nodes {
		if _, exists := byName[n.Name]; exists {
			return nil, fmt.Errorf("duplicate service name: %s", n.Name)
		}
		byName[n.Name] = n
	}
	for _, n := range byName {
		for _, dep := range n.DependsOn {
			if _, ok := byName[dep]; !ok {
				return nil, fmt.Errorf("service %q depends on unknown service %q", n.Name, dep)
			}
		}
	}

	order, err := topoSort(byName)
	if err != nil {
		return nil, err
	}

	return &Graph{nodes: byName, order: order}, nil
}

const (
	white = 0
	gray  = 1
	black = 2
)

func topoSort(nodes map[string]Node) ([]string, error) {
	state := make(map[string]int, len(nodes))
	order := make([]string, 0, len(nodes))

	var visit func(name string, stack []string) error
	visit = func(name string, stack []string) error {
		switch state[name] {
		case black:
			return nil
		case gray:
			return fmt.Errorf("dependency cycle detected: %v -> %s", stack, name)
		}
		state[name] = gray
		deps := append([]string(nil), nodes[name].DependsOn...)
		sort.Strings(deps)
		for _, dep := range deps {
			if err := visit(dep, append(stack, name)); err != nil {
				return err
			}
		}
		state[name] = black
		order = append(order, name)
		return nil
	}

	names := make([]string, 0, len(nodes))
	for name := range nodes {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if state[name] == white {
			if err := visit(name, []string{name}); err != nil {
				return nil, err
			}
		}
	}
	return order, nil
}

// Order returns service names in dependency order (dependencies first).
func (g *Graph) Order() []string { return append([]string(nil), g.order...) }

// DependsOn returns the direct dependencies of name.
func (g *Graph) DependsOn(name string) []string {
	return append([]string(nil), g.nodes[name].DependsOn...)
}

// Dependents returns the services that directly depend on name.
func (g *Graph) Dependents(name string) []string {
	var deps []string
	names := make([]string, 0, len(g.nodes))
	for n := range g.nodes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, d := range g.nodes[n].DependsOn {
			if d == name {
				deps = append(deps, n)
			}
		}
	}
	return deps
}

func (g *Graph) Has(name string) bool {
	_, ok := g.nodes[name]
	return ok
}
