// Package credentials resolves discovery credential references at connect
// time (DISCOVERY §1). Values live only in memory, in the pool config; they
// are never cached on disk, logged or returned by the API.
package credentials

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/Warewave-Technology/athens/internal/discovery"
)

// Credentials is a resolved user/password. Its String and LogValue never
// reveal the password.
type Credentials struct {
	User     string
	Password string
}

func (c Credentials) String() string { return "Credentials{User:" + c.User + ", Password:<redacted>}" }

func (c Credentials) LogValue() slog.Value {
	return slog.GroupValue(slog.String("user", c.User), slog.String("password", "<redacted>"))
}

// WorkloadLookup returns a cached pod template (discovery.Controller).
type WorkloadLookup interface {
	PodSpec(namespace string, ref discovery.ObjectRef) (*corev1.PodSpec, error)
}

type Resolver struct {
	client    kubernetes.Interface
	workloads WorkloadLookup
}

func NewResolver(client kubernetes.Interface, workloads WorkloadLookup) *Resolver {
	return &Resolver{client: client, workloads: workloads}
}

// ErrUnresolved means a reference points at something missing.
var ErrUnresolved = errors.New("credential reference unresolved")

// Resolve returns the user and password of ref in namespace.
func (r *Resolver) Resolve(ctx context.Context, namespace string, ref discovery.CredentialRef) (Credentials, error) {
	if ref.Password.Literal != "" || ref.Password.ConfigMap != nil {
		return Credentials{}, fmt.Errorf("%w: passwords are only read from Secrets or pod env", ErrUnresolved)
	}
	user, err := r.Value(ctx, namespace, ref.User)
	if err != nil {
		return Credentials{}, fmt.Errorf("user: %w", err)
	}
	pw, err := r.Value(ctx, namespace, ref.Password)
	if err != nil {
		return Credentials{}, fmt.Errorf("password: %w", err)
	}
	return Credentials{User: user, Password: pw}, nil
}

// DefaultDB resolves the database for the initial connection.
func (r *Resolver) DefaultDB(ctx context.Context, t discovery.Target) string {
	if t.DefaultDB != "" {
		return t.DefaultDB
	}
	if t.DefaultDBFrom != nil {
		if v, err := r.Value(ctx, t.Namespace, *t.DefaultDBFrom); err == nil && v != "" {
			return v
		}
	}
	return "postgres"
}

// CA returns the CA bundle for verify-* TLS modes.
func (r *Resolver) CA(ctx context.Context, namespace string, ref *discovery.KeyRef) ([]byte, error) {
	if ref == nil {
		return nil, nil
	}
	return r.secretKey(ctx, namespace, ref)
}

// Value resolves one ValueSource. Error messages name the object and key,
// never a value.
func (r *Resolver) Value(ctx context.Context, namespace string, v discovery.ValueSource) (string, error) {
	switch {
	case v.Literal != "":
		return v.Literal, nil
	case v.Secret != nil:
		b, err := r.secretKey(ctx, namespace, v.Secret)
		return string(b), err
	case v.ConfigMap != nil:
		cm, err := r.client.CoreV1().ConfigMaps(namespace).Get(ctx, v.ConfigMap.Name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("configmap %s/%s: %w", namespace, v.ConfigMap.Name, err)
		}
		val, ok := cm.Data[v.ConfigMap.Key]
		if !ok {
			return "", fmt.Errorf("%w: configmap %s/%s has no key %q", ErrUnresolved, namespace, v.ConfigMap.Name, v.ConfigMap.Key)
		}
		return val, nil
	case v.Env != nil:
		return r.envValue(namespace, v.Env)
	}
	return "", fmt.Errorf("%w: empty value source", ErrUnresolved)
}

func (r *Resolver) secretKey(ctx context.Context, namespace string, ref *discovery.KeyRef) ([]byte, error) {
	s, err := r.client.CoreV1().Secrets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("secret %s/%s: %w", namespace, ref.Name, err)
	}
	b, ok := s.Data[ref.Key]
	if !ok {
		return nil, fmt.Errorf("%w: secret %s/%s has no key %q", ErrUnresolved, namespace, ref.Name, ref.Key)
	}
	return b, nil
}

func (r *Resolver) envValue(namespace string, ref *discovery.EnvRef) (string, error) {
	if r.workloads == nil {
		return "", fmt.Errorf("%w: no workload cache", ErrUnresolved)
	}
	spec, err := r.workloads.PodSpec(namespace, ref.Workload)
	if err != nil {
		return "", fmt.Errorf("%s %s/%s: %w", ref.Workload.Kind, namespace, ref.Workload.Name, err)
	}
	for _, c := range spec.Containers {
		if c.Name != ref.Container {
			continue
		}
		val, found := "", false
		for _, e := range c.Env {
			if e.Name == ref.Name && e.ValueFrom == nil {
				val, found = e.Value, true
			}
		}
		if found {
			return val, nil
		}
	}
	return "", fmt.Errorf("%w: env %s not found in container %q", ErrUnresolved, ref.Name, ref.Container)
}
