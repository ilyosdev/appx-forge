package models

// NodeStatus represents the health/lifecycle status of a compute node.
type NodeStatus string

const (
	NodeHealthy   NodeStatus = "healthy"
	NodeUnhealthy NodeStatus = "unhealthy"
	NodeDraining  NodeStatus = "draining"
	NodeRemoved   NodeStatus = "removed"
)
