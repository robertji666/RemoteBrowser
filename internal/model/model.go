package model

import "time"

type User struct {
	ID                 int64
	Username           string
	Email              string
	DisplayName        string
	Role               string
	Status             string
	PasswordHash       string
	InstanceQuota      int
	InstanceCount      int
	MustChangePassword bool
	AuthVersion        int64
	DeliveryStatus     string
	DeliveryError      string
	LastError          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

const (
	RoleAdmin        = "admin"
	RoleUser         = "user"
	UserActive       = "active"
	UserDisabled     = "disabled"
	UserDeleting     = "deleting"
	UserDeleteFailed = "delete_failed"
)

type SessionStatus string

const (
	SessionCreating     SessionStatus = "creating"
	SessionStarting     SessionStatus = "starting"
	SessionRunning      SessionStatus = "running"
	SessionDisconnected SessionStatus = "disconnected"
	SessionStopping     SessionStatus = "stopping"
	SessionStopped      SessionStatus = "stopped"
	SessionExpired      SessionStatus = "expired"
	SessionError        SessionStatus = "error"
	SessionDeleting     SessionStatus = "deleting"
	SessionUnknown      SessionStatus = "unknown"
)

type Session struct {
	ID               string
	UserID           int64
	Status           SessionStatus
	ContainerName    string
	ProfileDir       string
	DownloadsDir     string
	SessionTokenHash string
	LastActiveAt     *time.Time
	CreatedAt        time.Time
	ExpiredAt        *time.Time
	Name             string
	DesiredState     string
	LastError        string
	CreateRequestKey string
	RecoveryAttempts int
	RecoveryNextAt   *time.Time
	ConnectedCount   int
}

type SessionEventType string

const (
	EventCreated      SessionEventType = "created"
	EventStarted      SessionEventType = "started"
	EventConnected    SessionEventType = "connected"
	EventDisconnected SessionEventType = "disconnected"
	EventStopped      SessionEventType = "stopped"
	EventExpired      SessionEventType = "expired"
	EventError        SessionEventType = "error"
)

type SessionEvent struct {
	ID          int64
	SessionID   string
	Type        SessionEventType
	PayloadJSON string
	CreatedAt   time.Time
}

type SessionFile struct {
	ID          int64
	SessionID   string
	Name        string
	SizeBytes   int64
	ContentType string
	CreatedAt   time.Time
}

type AuditEvent struct {
	ID           int64
	ActorID      int64
	TargetUserID int64
	Action       string
	TargetID     string
	Result       string
	Detail       string
	CreatedAt    time.Time
}

type MailDelivery struct {
	ID        int64
	UserID    int64
	Kind      string
	Status    string
	Error     string
	CreatedAt time.Time
}
