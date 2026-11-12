// Package discovery finds PostgreSQL targets in the configured namespaces
// (DISCOVERY.md). Targets hold references to credentials, never values.
package discovery

type SourceKind string

const (
	SourceAnnotation SourceKind = "annotation"
	SourceCNPG       SourceKind = "cnpg"
	SourceZalando    SourceKind = "zalando"
)

func (s SourceKind) priority() int {
	switch s {
	case SourceAnnotation:
		return 0
	case SourceCNPG:
		return 1
	default:
		return 2
	}
}

type ObjectRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	UID  string `json:"uid,omitempty"`
}

// KeyRef points at one key of a Secret or ConfigMap in the target namespace.
type KeyRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// EnvRef points at a literal env value in a workload's pod template. The
// value is read from the informer cache at connect time, never copied.
type EnvRef struct {
	Workload  ObjectRef `json:"workload"`
	Container string    `json:"container"`
	Name      string    `json:"name"`
}

// ValueSource says where one value (user, password, database) comes from.
// Exactly one field is set. Literal is only used for non-secret values
// (user names, database names); passwords are never Literal.
type ValueSource struct {
	Literal   string  `json:"literal,omitempty"`
	Secret    *KeyRef `json:"secret,omitempty"`
	ConfigMap *KeyRef `json:"configMap,omitempty"`
	Env       *EnvRef `json:"env,omitempty"`
}

func (v ValueSource) IsZero() bool {
	return v.Literal == "" && v.Secret == nil && v.ConfigMap == nil && v.Env == nil
}

// CredentialRef references a user/password pair (ADR-017).
type CredentialRef struct {
	User     ValueSource `json:"user"`
	Password ValueSource `json:"password"`
}

type Target struct {
	ID          string     `json:"id"` // "<namespace>/<name>"
	Namespace   string     `json:"namespace"`
	Name        string     `json:"name"`
	DisplayName string     `json:"displayName"`
	Source      SourceKind `json:"source"`
	Owner       ObjectRef  `json:"owner"`
	Service     string     `json:"service,omitempty"`
	Host        string     `json:"host"`
	Port        int        `json:"port"`
	Databases   []string   `json:"databases,omitempty"` // hint only
	// DefaultDB is the database for the initial connection. When empty,
	// DefaultDBFrom is resolved at connect time, then "postgres".
	DefaultDB     string            `json:"defaultDB,omitempty"`
	DefaultDBFrom *ValueSource      `json:"defaultDBFrom,omitempty"`
	SSLMode       string            `json:"sslMode"`
	CA            *KeyRef           `json:"ca,omitempty"` // Secret key with the CA bundle for verify-*
	Environment   string            `json:"environment,omitempty"`
	RW            CredentialRef     `json:"rw"`
	RO            *CredentialRef    `json:"ro,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Warnings      []string          `json:"warnings,omitempty"`
	// ReadOnly caps every user at read access (<prefix>/readonly, ADR-022).
	ReadOnly bool `json:"readOnly,omitempty"`
	// ReadOnlyRole is the role read users switch to (SET LOCAL ROLE) when
	// they run on the RW login; empty means pg_read_all_data (ADR-026).
	ReadOnlyRole string `json:"readOnlyRole,omitempty"`
	// Access, when set, replaces the config rules for this target
	// (<prefix>/access, ADR-022). An empty policy grants nobody.
	Access *AccessPolicy `json:"access,omitempty"`
}

// AccessPolicy lists who may use a target, from the access annotation.
type AccessPolicy struct {
	Grants []AccessGrant `json:"grants"`
}

// AccessGrant gives an Entra group (ID or authz.groupAliases name) a level.
type AccessGrant struct {
	Group string `json:"group"`
	Level string `json:"level"` // read | write
}

// Problem is a discovery issue that did not produce a target (or was
// dropped); shown on the admin discovery page.
type Problem struct {
	Namespace string     `json:"namespace"`
	Object    ObjectRef  `json:"object"`
	Source    SourceKind `json:"source"`
	Reason    string     `json:"reason"`
	Message   string     `json:"message"`
}
