package discovery

import (
	"fmt"
	"path"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

var (
	passwordEnvs = []string{"POSTGRES_PASSWORD", "POSTGRESQL_PASSWORD", "POSTGRES_POSTGRES_PASSWORD", "POSTGRESQL_POSTGRES_PASSWORD"}
	adminEnvs    = []string{"POSTGRES_POSTGRES_PASSWORD", "POSTGRESQL_POSTGRES_PASSWORD"}
	userEnvs     = []string{"POSTGRES_USER", "POSTGRESQL_USERNAME", "POSTGRESQL_USER"}
	dbEnvs       = []string{"POSTGRES_DB", "POSTGRES_DATABASE", "POSTGRESQL_DATABASE"}
)

type envResult struct {
	user, password, db ValueSource
	warnings           []string
}

type envResolver struct {
	spec     *corev1.PodSpec
	c        *corev1.Container
	owner    ObjectRef
	warnings []string
}

// resolveEnv implements DISCOVERY §4.4 for one container.
func resolveEnv(spec *corev1.PodSpec, c *corev1.Container, owner ObjectRef) envResult {
	r := &envResolver{spec: spec, c: c, owner: owner}
	var res envResult
	pwName := ""
	res.password, pwName = r.first(passwordEnvs, true)
	switch {
	case res.password.IsZero():
	case slices.Contains(adminEnvs, pwName):
		res.user = ValueSource{Literal: "postgres"}
	default:
		res.user, _ = r.first(userEnvs, false)
	}
	if res.user.IsZero() {
		res.user = ValueSource{Literal: "postgres"}
	}
	res.db, _ = r.first(dbEnvs, false)
	res.warnings = r.warnings
	return res
}

func (r *envResolver) warn(format string, a ...any) {
	w := fmt.Sprintf(format, a...)
	if !slices.Contains(r.warnings, w) {
		r.warnings = append(r.warnings, w)
	}
}

// first returns the first name that resolves. Explicit env vars and _FILE
// vars are definitive; envFrom is only a guess (the secret's keys are not
// listed, ADR-005), so it is tried after all names failed explicitly.
func (r *envResolver) first(names []string, secret bool) (ValueSource, string) {
	for _, n := range names {
		if v, ok := r.explicit(n, secret); ok {
			return v, n
		}
		if v, ok := r.file(n + "_FILE"); ok {
			return v, n
		}
	}
	for _, n := range names {
		if v, ok := r.envFrom(n, secret); ok {
			return v, n
		}
	}
	return ValueSource{}, ""
}

func (r *envResolver) explicit(name string, secret bool) (ValueSource, bool) {
	var e *corev1.EnvVar
	for i := range r.c.Env {
		if r.c.Env[i].Name == name {
			e = &r.c.Env[i] // the last definition wins
		}
	}
	if e == nil {
		return ValueSource{}, false
	}
	vf := e.ValueFrom
	switch {
	case vf == nil:
		if e.Value == "" {
			return ValueSource{}, false
		}
		if strings.Contains(e.Value, "$(") {
			r.warn("%s uses $(VAR) expansion, which is not supported", name)
			return ValueSource{}, false
		}
		if secret {
			r.warn("plaintext password in pod spec (%s)", name)
			return ValueSource{Env: &EnvRef{Workload: r.owner, Container: r.c.Name, Name: name}}, true
		}
		return ValueSource{Literal: e.Value}, true
	case vf.SecretKeyRef != nil:
		return ValueSource{Secret: &KeyRef{Name: vf.SecretKeyRef.Name, Key: vf.SecretKeyRef.Key}}, true
	case vf.ConfigMapKeyRef != nil:
		if secret {
			r.warn("password from ConfigMap ignored (%s)", name)
			return ValueSource{}, false
		}
		return ValueSource{ConfigMap: &KeyRef{Name: vf.ConfigMapKeyRef.Name, Key: vf.ConfigMapKeyRef.Key}}, true
	default:
		r.warn("%s uses an unsupported valueFrom source", name)
		return ValueSource{}, false
	}
}

func (r *envResolver) envFrom(name string, secret bool) (ValueSource, bool) {
	for i := len(r.c.EnvFrom) - 1; i >= 0; i-- { // later sources take precedence
		ef := r.c.EnvFrom[i]
		key, ok := strings.CutPrefix(name, ef.Prefix)
		if !ok || key == "" {
			continue
		}
		switch {
		case ef.SecretRef != nil:
			return ValueSource{Secret: &KeyRef{Name: ef.SecretRef.Name, Key: key}}, true
		case ef.ConfigMapRef != nil:
			if secret {
				r.warn("password from ConfigMap ignored (envFrom %s)", ef.ConfigMapRef.Name)
				continue
			}
			return ValueSource{ConfigMap: &KeyRef{Name: ef.ConfigMapRef.Name, Key: key}}, true
		}
	}
	return ValueSource{}, false
}

// file maps a <NAME>_FILE path onto a secret volume key.
func (r *envResolver) file(name string) (ValueSource, bool) {
	var p string
	for _, e := range r.c.Env {
		if e.Name == name && e.ValueFrom == nil {
			p = e.Value
		}
	}
	if p == "" {
		return ValueSource{}, false
	}
	p = path.Clean(p)
	var mount *corev1.VolumeMount
	rel := ""
	for i := range r.c.VolumeMounts {
		m := &r.c.VolumeMounts[i]
		mp := path.Clean(m.MountPath)
		switch {
		case m.SubPath != "" && p == mp:
			mount, rel = m, m.SubPath
		case m.SubPath == "" && strings.HasPrefix(p, mp+"/") && (mount == nil || len(mp) > len(path.Clean(mount.MountPath))):
			mount, rel = m, strings.TrimPrefix(p, mp+"/")
		}
	}
	if mount == nil {
		r.warn("%s=%s is not on a mounted volume", name, p)
		return ValueSource{}, false
	}
	for _, v := range r.spec.Volumes {
		if v.Name != mount.Name {
			continue
		}
		switch {
		case v.Secret != nil:
			if key, ok := itemKey(v.Secret.Items, rel); ok {
				return ValueSource{Secret: &KeyRef{Name: v.Secret.SecretName, Key: key}}, true
			}
		case v.Projected != nil:
			for _, src := range v.Projected.Sources {
				if src.Secret == nil {
					continue
				}
				if key, ok := itemKey(src.Secret.Items, rel); ok {
					return ValueSource{Secret: &KeyRef{Name: src.Secret.Name, Key: key}}, true
				}
			}
		default:
			r.warn("%s=%s is not on a secret volume", name, p)
			return ValueSource{}, false
		}
		r.warn("%s=%s does not map to a secret key", name, p)
		return ValueSource{}, false
	}
	r.warn("%s: volume %q not found", name, mount.Name)
	return ValueSource{}, false
}

// itemKey maps a file path inside a secret volume to the secret key.
func itemKey(items []corev1.KeyToPath, rel string) (string, bool) {
	if len(items) == 0 {
		return rel, !strings.Contains(rel, "/")
	}
	for _, it := range items {
		if path.Clean(it.Path) == rel {
			return it.Key, true
		}
	}
	return "", false
}
