package discovery

import (
	"cmp"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/Warewave-Technology/athens/internal/config"
)

// Options are the discovery settings Build needs. Only objects annotated
// <prefix>/enabled: "true" become targets (ADR-020, ADR-024).
type Options struct {
	AnnotationPrefix string
	ClusterDomain    string
	UseFQDN          bool
	DefaultSSLMode   string
	CNPG             config.CNPG
	Zalando          config.Zalando
	imageRE          *regexp.Regexp // picks the postgres container of a workload
}

func OptionsFromConfig(c config.Discovery) (Options, error) {
	re, err := regexp.Compile(c.ImagePattern)
	if err != nil {
		return Options{}, err
	}
	return Options{
		AnnotationPrefix: c.AnnotationPrefix, ClusterDomain: c.ClusterDomain, UseFQDN: c.UseFQDN,
		DefaultSSLMode: c.DefaultSSLMode, CNPG: c.CNPG, Zalando: c.Zalando, imageRE: re,
	}, nil
}

// Objects are the cached objects of one namespace.
type Objects struct {
	Namespace    string
	StatefulSets []*appsv1.StatefulSet
	Deployments  []*appsv1.Deployment
	Services     []*corev1.Service
	CNPG         []*unstructured.Unstructured
	Zalando      []*unstructured.Unstructured
}

var sslModes = map[string]bool{"disable": true, "allow": true, "prefer": true, "require": true, "verify-ca": true, "verify-full": true}

type candidate struct {
	t         Target
	kindShort string
	kindOrder int
}

type builder struct {
	opt      Options
	ns       string
	objs     Objects
	optedOut map[string]bool // service names with enabled=false
	cands    []candidate
	problems []Problem
}

// Build runs all sources over one namespace and merges the result (§3).
func Build(objs Objects, opt Options) ([]Target, []Problem) {
	b := &builder{opt: opt, ns: objs.Namespace, objs: objs, optedOut: map[string]bool{}}
	for _, s := range objs.Services {
		if v, _ := b.ann(s.ObjectMeta, "enabled"); v == "false" {
			b.optedOut[s.Name] = true
		}
	}
	for _, w := range b.workloads() {
		b.fromWorkload(w)
	}
	for _, s := range objs.Services {
		if v, _ := b.ann(s.ObjectMeta, "enabled"); v == "true" {
			b.fromAnnotatedService(s)
		}
	}
	if opt.CNPG.Enabled {
		for _, c := range objs.CNPG {
			b.fromCNPG(c)
		}
	}
	if opt.Zalando.Enabled {
		for _, z := range objs.Zalando {
			b.fromZalando(z)
		}
	}
	return b.merge(), b.problems
}

func (b *builder) ann(m metav1.ObjectMeta, key string) (string, bool) {
	v, ok := m.Annotations[b.opt.AnnotationPrefix+"/"+key]
	return strings.TrimSpace(v), ok
}

func (b *builder) annOr(m metav1.ObjectMeta, key, def string) string {
	if v, ok := b.ann(m, key); ok && v != "" {
		return v
	}
	return def
}

func (b *builder) problem(obj ObjectRef, src SourceKind, reason, format string, a ...any) {
	b.problems = append(b.problems, Problem{Namespace: b.ns, Object: obj, Source: src, Reason: reason, Message: fmt.Sprintf(format, a...)})
}

func (b *builder) host(svc string) string {
	h := svc + "." + b.ns + ".svc"
	if b.opt.UseFQDN {
		h += "." + b.opt.ClusterDomain
	}
	return h
}

// ---- workloads (annotation on workload, generic) ----

type workload struct {
	kind, kindShort string
	order           int
	meta            metav1.ObjectMeta
	tmpl            corev1.PodTemplateSpec
}

func (w workload) ref() ObjectRef {
	return ObjectRef{Kind: w.kind, Name: w.meta.Name, UID: string(w.meta.UID)}
}

func (b *builder) workloads() []workload {
	var out []workload
	for _, s := range b.objs.StatefulSets {
		out = append(out, workload{kind: "StatefulSet", kindShort: "sts", order: 1, meta: s.ObjectMeta, tmpl: s.Spec.Template})
	}
	for _, d := range b.objs.Deployments {
		out = append(out, workload{kind: "Deployment", kindShort: "deploy", order: 2, meta: d.ObjectMeta, tmpl: d.Spec.Template})
	}
	return out
}

// imageRepo strips registry, tag and digest: "docker.io/bitnami/postgresql:16" -> "bitnami/postgresql".
func imageRepo(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	if first, rest, ok := strings.Cut(image, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		image = rest
	}
	return image
}

func (b *builder) pgContainer(spec *corev1.PodSpec) *corev1.Container {
	for i := range spec.Containers {
		if b.opt.imageRE != nil && b.opt.imageRE.MatchString(imageRepo(spec.Containers[i].Image)) {
			return &spec.Containers[i]
		}
	}
	return nil
}

func containerPort(c *corev1.Container) corev1.ContainerPort {
	for _, p := range c.Ports {
		switch p.Name {
		case "postgresql", "postgres", "tcp-postgresql":
			return p
		}
	}
	for _, p := range c.Ports {
		if p.ContainerPort == 5432 {
			return p
		}
	}
	return corev1.ContainerPort{ContainerPort: 5432}
}

func (b *builder) fromWorkload(w workload) {
	if enabled, _ := b.ann(w.meta, "enabled"); enabled != "true" {
		return
	}
	c := b.pgContainer(&w.tmpl.Spec)
	if c == nil {
		if len(w.tmpl.Spec.Containers) == 0 {
			b.problem(w.ref(), SourceAnnotation, "no_container", "workload has no containers")
			return
		}
		c = &w.tmpl.Spec.Containers[0]
	}
	src := SourceAnnotation
	t := Target{Namespace: b.ns, Name: w.meta.Name, Source: src, Owner: w.ref(), Labels: w.meta.Labels}

	// service + port
	cport := containerPort(c)
	svcName, _ := b.ann(w.meta, "service")
	if svcName != "" {
		svc := b.service(svcName)
		if svc == nil {
			b.problem(w.ref(), src, "service_not_found", "annotated service %q does not exist", svcName)
			return
		}
		t.Service, t.Host, t.Port = svc.Name, b.host(svc.Name), servicePortFor(svc, cport)
	} else {
		m := b.matchService(w.tmpl.Labels, cport)
		if m == nil {
			b.problem(w.ref(), src, "no_service", "no Service selects the pods and exposes port %s", portString(cport))
			return
		}
		t.Service, t.Port = m.svc.Name, int(m.port.Port)
		if m.headless && w.kind == "StatefulSet" {
			t.Host = w.meta.Name + "-0." + b.host(m.svc.Name)
			t.Warnings = append(t.Warnings, fmt.Sprintf("only headless service %q matches; assuming pod %s-0 is the primary", m.svc.Name, w.meta.Name))
		} else {
			t.Host = b.host(m.svc.Name)
			if m.headless {
				t.Warnings = append(t.Warnings, fmt.Sprintf("only headless service %q matches", m.svc.Name))
			}
		}
	}
	if b.optedOut[t.Service] {
		return
	}
	if p, ok := b.ann(w.meta, "port"); ok && p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			t.Port = n
		} else {
			t.Warnings = append(t.Warnings, fmt.Sprintf("invalid port annotation %q ignored", p))
		}
	}

	// credentials
	if secret, ok := b.ann(w.meta, "secret"); ok && secret != "" {
		b.secretCreds(&t, w.meta, secret)
	} else {
		r := resolveEnv(&w.tmpl.Spec, c, w.ref())
		t.Warnings = append(t.Warnings, r.warnings...)
		if r.password.IsZero() {
			b.problem(w.ref(), src, "credentials_unresolved", "no usable password in container %q: %s", c.Name, strings.Join(r.warnings, "; "))
			return
		}
		t.RW = CredentialRef{User: r.user, Password: r.password}
		switch {
		case !r.db.IsZero() && r.db.Literal != "":
			t.DefaultDB = r.db.Literal
		case !r.db.IsZero():
			db := r.db
			t.DefaultDBFrom = &db
		case r.user.Literal != "":
			t.DefaultDB = r.user.Literal
		default:
			u := r.user
			t.DefaultDBFrom = &u
		}
	}
	b.applyCommonAnnotations(&t, w.meta)
	b.cands = append(b.cands, candidate{t: t, kindShort: w.kindShort, kindOrder: w.order})
}

func (b *builder) service(name string) *corev1.Service {
	for _, s := range b.objs.Services {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func portString(p corev1.ContainerPort) string {
	if p.Name != "" {
		return fmt.Sprintf("%d (%s)", p.ContainerPort, p.Name)
	}
	return strconv.Itoa(int(p.ContainerPort))
}

func targetPortMatches(sp corev1.ServicePort, cp corev1.ContainerPort) bool {
	tp := sp.TargetPort
	if tp.Type == intstr.String {
		return tp.StrVal != "" && tp.StrVal == cp.Name
	}
	n := tp.IntVal
	if n == 0 {
		n = sp.Port
	}
	return n == cp.ContainerPort
}

// servicePortFor returns the service port forwarding to the container port,
// else the named postgres port, else 5432.
func servicePortFor(svc *corev1.Service, cp corev1.ContainerPort) int {
	for _, p := range svc.Spec.Ports {
		if targetPortMatches(p, cp) {
			return int(p.Port)
		}
	}
	for _, p := range svc.Spec.Ports {
		switch p.Name {
		case "postgres", "postgresql", "tcp-postgresql":
			return int(p.Port)
		}
	}
	return 5432
}

type svcMatch struct {
	svc      *corev1.Service
	port     corev1.ServicePort
	headless bool
}

var secondarySuffixes = []string{"-hl", "-headless", "-repl", "-replica", "-read", "-ro"}

func isSecondaryName(n string) bool {
	return slices.ContainsFunc(secondarySuffixes, func(s string) bool { return strings.HasSuffix(n, s) })
}

// matchService implements §4.5.
func (b *builder) matchService(podLabels map[string]string, cp corev1.ContainerPort) *svcMatch {
	var ms []svcMatch
	for _, s := range b.objs.Services {
		sel := s.Spec.Selector
		if len(sel) == 0 {
			continue
		}
		subset := true
		for k, v := range sel {
			if podLabels[k] != v {
				subset = false
				break
			}
		}
		if !subset {
			continue
		}
		for _, p := range s.Spec.Ports {
			if targetPortMatches(p, cp) {
				ms = append(ms, svcMatch{svc: s, port: p, headless: s.Spec.ClusterIP == corev1.ClusterIPNone})
				break
			}
		}
	}
	if len(ms) == 0 {
		return nil
	}
	slices.SortFunc(ms, func(a, c svcMatch) int {
		if a.headless != c.headless {
			return boolCmp(a.headless, c.headless)
		}
		if sa, sc := isSecondaryName(a.svc.Name), isSecondaryName(c.svc.Name); sa != sc {
			return boolCmp(sa, sc)
		}
		return cmp.Or(cmp.Compare(len(a.svc.Name), len(c.svc.Name)), strings.Compare(a.svc.Name, c.svc.Name))
	})
	return &ms[0]
}

func boolCmp(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	default:
		return -1
	}
}

// secretCreds sets RW credentials from the annotation secret (+user/keys).
func (b *builder) secretCreds(t *Target, m metav1.ObjectMeta, secret string) {
	user := ValueSource{Secret: &KeyRef{Name: secret, Key: b.annOr(m, "user-key", "username")}}
	if u, ok := b.ann(m, "user"); ok && u != "" {
		user = ValueSource{Literal: u}
	}
	t.RW = CredentialRef{User: user, Password: ValueSource{Secret: &KeyRef{Name: secret, Key: b.annOr(m, "password-key", "password")}}}
	if user.Literal != "" {
		t.DefaultDB = user.Literal
	} else {
		u := user
		t.DefaultDBFrom = &u
	}
}

// applyCommonAnnotations handles database, display-name, environment,
// sslmode, CA and the read-only credential.
func (b *builder) applyCommonAnnotations(t *Target, m metav1.ObjectMeta) {
	if db, ok := b.ann(m, "database"); ok && db != "" {
		t.DefaultDB, t.DefaultDBFrom = db, nil
	}
	t.DisplayName = b.annOr(m, "display-name", t.Name)
	t.Environment = b.annOr(m, "environment", t.Environment)
	if t.SSLMode == "" {
		t.SSLMode = b.opt.DefaultSSLMode
	}
	if mode, ok := b.ann(m, "sslmode"); ok && mode != "" {
		if sslModes[mode] {
			t.SSLMode = mode
		} else {
			t.Warnings = append(t.Warnings, fmt.Sprintf("invalid sslmode annotation %q ignored", mode))
		}
	}
	if ca, ok := b.ann(m, "ca-secret"); ok && ca != "" {
		t.CA = &KeyRef{Name: ca, Key: b.annOr(m, "ca-key", "ca.crt")}
	}
	if ro, ok := b.ann(m, "readonly-secret"); ok && ro != "" {
		t.RO = &CredentialRef{
			User:     ValueSource{Secret: &KeyRef{Name: ro, Key: b.annOr(m, "readonly-user-key", "username")}},
			Password: ValueSource{Secret: &KeyRef{Name: ro, Key: b.annOr(m, "readonly-password-key", "password")}},
		}
	}
	if v, ok := b.ann(m, "readonly-provision"); ok {
		switch strings.ToLower(v) {
		case "true":
			t.ROProvision = t.RO == nil
			if t.RO != nil {
				t.Warnings = append(t.Warnings, "readonly-provision ignored: readonly-secret is set")
			}
		case "false", "":
		default:
			t.Warnings = append(t.Warnings, fmt.Sprintf("readonly-provision annotation %q is not true/false; ignored", v))
		}
	}
	if strings.HasPrefix(t.SSLMode, "verify-") && t.CA == nil {
		t.Warnings = append(t.Warnings, "sslmode "+t.SSLMode+" without a CA secret uses the system roots")
	}
	b.applyAccess(t, m)
	if t.RO == nil && !t.ROProvision && t.Access != nil && len(t.Access.Grants) > 0 && (t.ReadOnly || slices.ContainsFunc(t.Access.Grants, func(g AccessGrant) bool { return g.Level == "read" })) {
		t.Warnings = append(t.Warnings, fmt.Sprintf("read users cannot connect: no read-only login; add %[1]s/readonly-secret or %[1]s/readonly-provision", b.opt.AnnotationPrefix))
	}
}

// applyAccess reads the readonly and access annotations (ADR-022, ADR-023).
// Both fail closed: an unreadable readonly value means read-only, invalid
// access entries are dropped, and without an access annotation nobody can use
// the target.
func (b *builder) applyAccess(t *Target, m metav1.ObjectMeta) {
	if v, ok := b.ann(m, "readonly"); ok {
		switch strings.ToLower(v) {
		case "true":
			t.ReadOnly = true
		case "false", "":
		default:
			t.ReadOnly = true
			t.Warnings = append(t.Warnings, fmt.Sprintf("readonly annotation %q is not true/false; treating the target as read-only", v))
		}
	}
	v, ok := b.ann(m, "access")
	if !ok {
		t.Warnings = append(t.Warnings, fmt.Sprintf("no %s/access annotation; nobody can use this target", b.opt.AnnotationPrefix))
		return
	}
	p, warnings := ParseAccess(v)
	t.Access = &p
	t.Warnings = append(t.Warnings, warnings...)
	if len(p.Grants) == 0 {
		t.Warnings = append(t.Warnings, "access annotation grants no group; nobody can use this target")
	}
}

// ParseAccess parses "group:rw,other:ro" (comma or whitespace separated;
// rw|write, ro|read). The last ":" separates the level, so group names may
// contain colons. A group listed twice gets the higher level.
func ParseAccess(v string) (AccessPolicy, []string) {
	var warnings []string
	levels := map[string]string{}
	var order []string
	for _, e := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' || unicode.IsSpace(r) }) {
		i := strings.LastIndex(e, ":")
		if i <= 0 {
			warnings = append(warnings, fmt.Sprintf("access entry %q ignored: expected group:rw or group:ro", e))
			continue
		}
		group, lvl := e[:i], ""
		switch strings.ToLower(e[i+1:]) {
		case "rw", "write":
			lvl = "write"
		case "ro", "read":
			lvl = "read"
		default:
			warnings = append(warnings, fmt.Sprintf("access entry %q ignored: level must be rw or ro", e))
			continue
		}
		if prev, seen := levels[group]; !seen {
			order = append(order, group)
		} else if prev == "write" {
			continue
		}
		levels[group] = lvl
	}
	p := AccessPolicy{Grants: []AccessGrant{}}
	for _, g := range order {
		p.Grants = append(p.Grants, AccessGrant{Group: g, Level: levels[g]})
	}
	return p, warnings
}

// ---- annotated Service ----

func (b *builder) fromAnnotatedService(s *corev1.Service) {
	ref := ObjectRef{Kind: "Service", Name: s.Name, UID: string(s.UID)}
	t := Target{Namespace: b.ns, Name: s.Name, Source: SourceAnnotation, Owner: ref, Service: s.Name, Host: b.host(s.Name),
		Port: servicePortFor(s, corev1.ContainerPort{ContainerPort: -1}), Labels: s.Labels}
	if p, ok := b.ann(s.ObjectMeta, "port"); ok && p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			t.Port = n
		} else {
			t.Warnings = append(t.Warnings, fmt.Sprintf("invalid port annotation %q ignored", p))
		}
	}
	secret, _ := b.ann(s.ObjectMeta, "secret")
	if secret == "" {
		b.problem(ref, SourceAnnotation, "credentials_unresolved", "annotated Service needs the %s/secret annotation", b.opt.AnnotationPrefix)
		return
	}
	b.secretCreds(&t, s.ObjectMeta, secret)
	b.applyCommonAnnotations(&t, s.ObjectMeta)
	b.cands = append(b.cands, candidate{t: t, kindShort: "svc", kindOrder: 0})
}

// ---- CloudNativePG ----

// optedIn reports the enabled annotation of an operator cluster.
func (b *builder) optedIn(meta metav1.ObjectMeta) bool {
	v, _ := b.ann(meta, "enabled")
	return v == "true"
}

func (b *builder) fromCNPG(u *unstructured.Unstructured) {
	meta := metaOf(u)
	if !b.optedIn(meta) {
		return
	}
	name := u.GetName()
	ref := ObjectRef{Kind: "Cluster", Name: name, UID: string(u.GetUID())}
	secret := name + "-app"
	if s, _, _ := unstructured.NestedString(u.Object, "spec", "bootstrap", "initdb", "secret", "name"); s != "" {
		secret = s
	}
	if b.opt.CNPG.UseSuperuser {
		if on, _, _ := unstructured.NestedBool(u.Object, "spec", "enableSuperuserAccess"); on {
			secret = name + "-superuser"
		}
	}
	db, _, _ := unstructured.NestedString(u.Object, "spec", "bootstrap", "initdb", "database")
	if db == "" {
		db = "app"
	}
	t := Target{
		Namespace: b.ns, Name: name, Source: SourceCNPG, Owner: ref, Service: name + "-rw", Host: b.host(name + "-rw"), Port: 5432,
		Databases: []string{db}, DefaultDB: db, SSLMode: "require", Labels: u.GetLabels(),
		RW: CredentialRef{
			User:     ValueSource{Secret: &KeyRef{Name: secret, Key: "username"}},
			Password: ValueSource{Secret: &KeyRef{Name: secret, Key: "password"}},
		},
	}
	if b.opt.CNPG.VerifyTLS {
		t.SSLMode = "verify-full"
		t.CA = &KeyRef{Name: name + "-ca", Key: "ca.crt"}
	}
	b.applyCommonAnnotations(&t, meta)
	b.cands = append(b.cands, candidate{t: t, kindShort: "cnpg", kindOrder: 3})
}

// ---- Zalando postgres-operator ----

func (b *builder) fromZalando(u *unstructured.Unstructured) {
	meta := metaOf(u)
	if !b.optedIn(meta) {
		return
	}
	name := u.GetName()
	ref := ObjectRef{Kind: "postgresql", Name: name, UID: string(u.GetUID())}
	dbs, _, _ := unstructured.NestedStringMap(u.Object, "spec", "databases")
	dbNames := make([]string, 0, len(dbs))
	for d := range dbs {
		dbNames = append(dbNames, d)
	}
	slices.Sort(dbNames)

	user, _ := b.ann(meta, "user")
	if user == "" && len(dbNames) > 0 {
		user = dbs[dbNames[0]]
	}
	if user == "" && b.opt.Zalando.UseSuperuser {
		user = "postgres"
	}
	if user == "" {
		b.problem(ref, SourceZalando, "credentials_unresolved",
			"no database owner in spec.databases and discovery.zalando.useSuperuser is false; set the %s/user annotation", b.opt.AnnotationPrefix)
		return
	}
	secret := strings.NewReplacer("{username}", strings.ReplaceAll(user, "_", "-"), "{cluster}", name).Replace(b.opt.Zalando.SecretTemplate)
	t := Target{
		Namespace: b.ns, Name: name, Source: SourceZalando, Owner: ref, Service: name, Host: b.host(name), Port: 5432,
		Databases: dbNames, SSLMode: "require", Labels: u.GetLabels(),
		RW: CredentialRef{
			User:     ValueSource{Secret: &KeyRef{Name: secret, Key: "username"}},
			Password: ValueSource{Secret: &KeyRef{Name: secret, Key: "password"}},
		},
	}
	if len(dbNames) > 0 {
		t.DefaultDB = dbNames[0]
	} else {
		t.DefaultDB = "postgres"
	}
	b.applyCommonAnnotations(&t, meta)
	b.cands = append(b.cands, candidate{t: t, kindShort: "zalando", kindOrder: 4})
}

func metaOf(u *unstructured.Unstructured) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: u.GetName(), Namespace: u.GetNamespace(), UID: u.GetUID(), Labels: u.GetLabels(), Annotations: u.GetAnnotations()}
}

// ---- merge (§3) ----

func (b *builder) merge() []Target {
	slices.SortStableFunc(b.cands, func(x, y candidate) int {
		return cmp.Or(cmp.Compare(x.t.Source.priority(), y.t.Source.priority()), cmp.Compare(x.kindOrder, y.kindOrder),
			strings.Compare(x.t.Name, y.t.Name))
	})
	seen := map[string]bool{}
	var kept []candidate
	for _, c := range b.cands {
		key := fmt.Sprintf("%s|%s|%d", c.t.Namespace, c.t.Host, c.t.Port)
		if seen[key] {
			continue // a higher-priority source already covers this endpoint
		}
		seen[key] = true
		kept = append(kept, c)
	}
	byName := map[string]int{}
	for _, c := range kept {
		byName[c.t.Name]++
	}
	out := make([]Target, 0, len(kept))
	for _, c := range kept {
		t := c.t
		if byName[t.Name] > 1 {
			t.Warnings = append(t.Warnings, fmt.Sprintf("name %q is used by several targets; renamed to %q", t.Name, t.Name+"-"+c.kindShort))
			if t.DisplayName == t.Name {
				t.DisplayName = t.Name + "-" + c.kindShort
			}
			t.Name += "-" + c.kindShort
		}
		t.ID = path.Join(t.Namespace, t.Name)
		out = append(out, t)
	}
	slices.SortFunc(out, func(x, y Target) int { return strings.Compare(x.ID, y.ID) })
	return out
}
