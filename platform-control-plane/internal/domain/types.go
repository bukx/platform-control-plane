package domain

import "time"

type RequestStatus string

const (
	StatusPendingApproval RequestStatus = "pending_approval"
	StatusApproved        RequestStatus = "approved"
	StatusQueued          RequestStatus = "queued"
	StatusReconciling     RequestStatus = "reconciling"
	StatusReady           RequestStatus = "ready"
	StatusFailed          RequestStatus = "failed"
)

type EnvironmentClass struct {
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	AllowedRegions    []string `json:"allowed_regions"`
	RequiresApproval  bool     `json:"requires_approval"`
	MaxTTLHours       int      `json:"max_ttl_hours"`
	DefaultNamespaces int      `json:"default_namespaces"`
}

type EnvironmentRequest struct {
	ID                string            `json:"id"`
	App               string            `json:"app"`
	Team              string            `json:"team"`
	Class             string            `json:"class"`
	Region            string            `json:"region"`
	TTLHours          int               `json:"ttl_hours"`
	Owner             string            `json:"owner"`
	Repository        string            `json:"repository"`
	Revision          string            `json:"revision"`
	Labels            map[string]string `json:"labels,omitempty"`
	Namespace         string            `json:"namespace,omitempty"`
	GitOpsPath        string            `json:"gitops_path,omitempty"`
	LastError         string            `json:"last_error,omitempty"`
	Status            RequestStatus     `json:"status"`
	Version           int               `json:"version"`
	CreatedAt         time.Time         `json:"created_at"`
	QueuedAt          *time.Time        `json:"queued_at,omitempty"`
	ApprovedAt        *time.Time        `json:"approved_at,omitempty"`
	ApprovedBy        string            `json:"approved_by,omitempty"`
	ApprovalSignature string            `json:"approval_signature,omitempty"`
	LastReconciledAt  *time.Time        `json:"last_reconciled_at,omitempty"`
	GitCommitSHA      string            `json:"git_commit_sha,omitempty"`
	GitBranch         string            `json:"git_branch,omitempty"`
}

type CreateRequestInput struct {
	App        string            `json:"app"`
	Team       string            `json:"team"`
	Class      string            `json:"class"`
	Region     string            `json:"region"`
	TTLHours   int               `json:"ttl_hours"`
	Owner      string            `json:"owner"`
	Repository string            `json:"repository"`
	Revision   string            `json:"revision"`
	Labels     map[string]string `json:"labels,omitempty"`
}
